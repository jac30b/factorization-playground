package engine

import (
	"encoding/binary"
	"hash"
	"hash/fnv"

	"github.com/bits-and-blooms/bloom/v3"
)

const (
	pointerBits = 48
	pointerMask = (uint64(1) << pointerBits) - 1
	saltShift   = pointerBits
)

// Key separates a join key's semantic equality from its hash value. Equal is
// required because different keys may have the same hash.
type Key interface {
	WriteHash(hash.Hash)
	Equal(Key) bool
}

// IntKey is the current key implementation for integer CSV columns.
type IntKey int64

func (key IntKey) WriteHash(writer hash.Hash) {
	var bytes [8]byte
	binary.LittleEndian.PutUint64(bytes[:], uint64(key))
	_, _ = writer.Write(bytes[:])
}

func (key IntKey) Equal(other Key) bool {
	otherKey, ok := other.(IntKey)
	return ok && key == otherKey
}

// BucketMetadata occupies exactly 64 bits:
//
//	[ 16-bit Bloom filter salt | 48-bit pointer to the distinct-key group ]
//
// The pointer is a one-based index into HashTable.groups, rather than a raw Go
// address. Zero means the bucket is empty. This retains a compact 48-bit
// physical layout without using unsafe pointers.
type BucketMetadata uint64

func newBucketMetadata(salt uint16, head uint64) BucketMetadata {
	if head > pointerMask {
		panic("hash-table group arena exceeds 48-bit address space")
	}
	return BucketMetadata(uint64(salt)<<saltShift | head)
}

func (metadata BucketMetadata) Salt() uint16 {
	return uint16(uint64(metadata) >> saltShift)
}

// mayContainSalt rejects keys whose Bloom bits are absent. A positive result
// still requires a full hash and key equality check.
func (metadata BucketMetadata) mayContainSalt(salt uint16) bool {
	return metadata.Salt()&salt == salt
}

func (metadata BucketMetadata) Head() uint64 {
	return uint64(metadata) & pointerMask
}

// keyGroup stores key metadata once, shared by every row with that key.
type keyGroup struct {
	hash  uint64
	key   Key
	head  uint64 // one-based index into HashTable.nodes; zero ends the row chain
	count int
}

// rowReference retains the row map without copying its payload.
type rowReference struct {
	row  Row
	next uint64 // one-based index into HashTable.nodes; zero ends the row chain
}

// HashTable is a linear-chained hash table. Linear probing resolves collisions
// between different keys. Each occupied bucket points to one distinct-key group,
// which owns a linked list of references to rows with that key.
// The zero value is ready to use.
type HashTable struct {
	buckets []BucketMetadata
	groups  []keyGroup
	nodes   []rowReference
}

func NewHashTable(capacity int) *HashTable {
	if capacity < 1 {
		capacity = 1
	}
	return &HashTable{buckets: make([]BucketMetadata, capacity)}
}

func (table *HashTable) Insert(key Key, row Row) {
	hash := hashKey(key)
	salt := saltFromHash(hash)

retry:
	for probes := 0; probes < len(table.buckets); probes++ {
		index := table.bucketIndex(hash, probes)
		metadata := table.buckets[index]
		head := metadata.Head()
		if head == 0 {
			if table.wouldExceedLoadFactor() {
				table.grow()
				goto retry
			}
			groupID := uint64(len(table.groups)) + 1
			if groupID > pointerMask {
				panic("hash-table group arena exceeds 48-bit address space")
			}
			table.groups = append(table.groups, keyGroup{
				hash: hash, key: key, head: table.appendNode(row, 0), count: 1,
			})
			table.buckets[index] = newBucketMetadata(salt, groupID)
			return
		}
		if !metadata.mayContainSalt(salt) {
			continue
		}
		group := table.group(head)
		if group.hash == hash && group.key.Equal(key) {
			group.head = table.appendNode(row, group.head)
			group.count++
			return
		}
	}
	table.grow()
	goto retry
}

func (table *HashTable) Lookup(key Key) []Row {
	id := table.lookupGroup(key)
	if id == 0 {
		return nil
	}
	group := table.group(id)
	matches := make([]Row, 0, group.count)
	for i := group.head; i != 0; i = table.node(i).next {
		matches = append(matches, table.node(i).row)
	}
	return matches
}

// lookupGroup returns a stable one-based group index without allocating or
// expanding the duplicate chain. Zero means there is no matching group.
func (table *HashTable) lookupGroup(key Key) uint64 {
	if len(table.buckets) == 0 {
		return 0
	}
	hash := hashKey(key)
	salt := saltFromHash(hash)
	for probes := 0; probes < len(table.buckets); probes++ {
		metadata := table.buckets[table.bucketIndex(hash, probes)]
		head := metadata.Head()
		if head == 0 {
			return 0
		}
		if !metadata.mayContainSalt(salt) {
			continue
		}
		group := table.group(head)
		if group.hash == hash && group.key.Equal(key) {
			return head
		}
	}
	return 0
}

func (table *HashTable) appendNode(row Row, next uint64) uint64 {
	address := uint64(len(table.nodes)) + 1
	if address > pointerMask {
		panic("hash-table row arena exceeds 48-bit address space")
	}
	table.nodes = append(table.nodes, rowReference{row: row, next: next})
	return address
}

func (table *HashTable) node(address uint64) *rowReference {
	return &table.nodes[address-1]
}

func (table *HashTable) group(address uint64) *keyGroup {
	return &table.groups[address-1]
}

func (table *HashTable) bucketIndex(hash uint64, probes int) int {
	// Callers only probe nonempty bucket slices. Modulo guarantees an index in
	// [0, len(table.buckets)), including when the unsigned addition wraps.
	return int((hash + uint64(probes)) % uint64(len(table.buckets)))
}

func (table *HashTable) wouldExceedLoadFactor() bool {
	return (len(table.groups)+1)*10 > len(table.buckets)*7
}

func (table *HashTable) grow() {
	oldBuckets := table.buckets
	capacity := len(oldBuckets) * 2
	if capacity == 0 {
		capacity = 1
	}
	table.buckets = make([]BucketMetadata, capacity)
	for _, metadata := range oldBuckets {
		head := metadata.Head()
		if head == 0 {
			continue
		}
		group := table.group(head)
		for probes := 0; ; probes++ {
			index := table.bucketIndex(group.hash, probes)
			if table.buckets[index].Head() == 0 {
				table.buckets[index] = newBucketMetadata(saltFromHash(group.hash), head)
				break
			}
		}
	}
}

func hashKey(key Key) uint64 {
	hasher := fnv.New64a()
	key.WriteHash(hasher)
	return hasher.Sum64()
}

func saltFromHash(hash uint64) uint16 {
	var data [8]byte
	binary.LittleEndian.PutUint64(data[:], hash)
	// Use one backing word but restrict the filter to the 16 salt bits. Two
	// hash locations keep the single-key filter sparse without extra metadata.
	var word [1]uint64
	filter := bloom.FromWithM(word[:], 16, 2)
	filter.Add(data[:])
	return uint16(word[0])
}
