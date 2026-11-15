package engine

import (
	"hash"
	"testing"
)

// collidingKey forces full hash collisions while retaining distinct keys.
type collidingKey int64

func (key collidingKey) WriteHash(writer hash.Hash) {
	_, _ = writer.Write([]byte{0})
}

func (key collidingKey) Equal(other Key) bool {
	otherKey, ok := other.(collidingKey)
	return ok && key == otherKey
}

func TestHashTableZeroValue(t *testing.T) {
	var table HashTable
	if rows := table.Lookup(collidingKey(1)); rows != nil {
		t.Fatalf("empty lookup returned %v", rows)
	}
	// Exercise initialization, probing, growth, and duplicate chains together.
	for key := collidingKey(0); key < 32; key++ {
		table.Insert(key, Row{"value": int64(key)})
		table.Insert(key, Row{"value": int64(key)})
	}
	for key := collidingKey(0); key < 32; key++ {
		rows := table.Lookup(key)
		if len(rows) != 2 || rows[0]["value"] != int64(key) || rows[1]["value"] != int64(key) {
			t.Fatalf("lookup(%d) = %v, want two matching rows", key, rows)
		}
	}
	if rows := table.Lookup(collidingKey(32)); rows != nil {
		t.Fatalf("missing lookup returned %v", rows)
	}
}

func TestHashTableGroupsSurviveCollisionsAndGrowth(t *testing.T) {
	table := NewHashTable(2)
	const keys = 64
	const duplicates = 8
	for round := 0; round < duplicates; round++ {
		for key := 0; key < keys; key++ {
			table.Insert(collidingKey(key), Row{"key": int64(key), "round": int64(round)})
		}
	}
	if len(table.groups) != keys {
		t.Fatalf("groups = %d, want %d", len(table.groups), keys)
	}
	if len(table.nodes) != keys*duplicates {
		t.Fatalf("row references = %d, want %d", len(table.nodes), keys*duplicates)
	}
	for key := 0; key < keys; key++ {
		rows := table.Lookup(collidingKey(key))
		if len(rows) != duplicates {
			t.Fatalf("lookup(%d) returned %d rows, want %d", key, len(rows), duplicates)
		}
		for index, row := range rows {
			if row["key"] != int64(key) || row["round"] != int64(duplicates-1-index) {
				t.Fatalf("lookup(%d)[%d] = %v, want key %d, round %d", key, index, row, key, duplicates-1-index)
			}
		}
	}
	if rows := table.Lookup(collidingKey(keys)); rows != nil {
		t.Fatalf("missing colliding key returned %v", rows)
	}
}

func TestHashTableDuplicatesKeepGroupAndBucket(t *testing.T) {
	table := NewHashTable(2)
	key := IntKey(7)
	table.Insert(key, Row{"value": 0})
	index := table.bucketIndex(hashKey(key), 0)
	metadata := table.buckets[index]
	bucketCount := len(table.buckets)
	for value := int64(1); value < 1024; value++ {
		table.Insert(key, Row{"value": value})
	}
	if len(table.groups) != 1 || len(table.buckets) != bucketCount || table.buckets[index] != metadata {
		t.Fatal("duplicate inserts changed the distinct-key group or bucket directory")
	}
	if rows := table.Lookup(key); len(rows) != 1024 {
		t.Fatalf("lookup returned %d rows, want 1024", len(rows))
	}
}

func TestHashTablePreservesDuplicateKeys(t *testing.T) {
	table := NewHashTable(2)
	table.Insert(IntKey(7), Row{"value": 1})
	table.Insert(IntKey(7), Row{"value": 2})

	matches := table.Lookup(IntKey(7))
	if got, want := len(matches), 2; got != want {
		t.Fatalf("matches = %d, want %d", got, want)
	}
	if matches := table.Lookup(IntKey(99)); len(matches) != 0 {
		t.Fatalf("missing key returned %d rows, want none", len(matches))
	}
}

func TestHashTableSeparatesBucketCollisions(t *testing.T) {
	table := NewHashTable(4)
	first, second := sameInitialBucket(4)
	table.Insert(first, Row{"value": 1})
	table.Insert(second, Row{"value": 2})

	matches := table.Lookup(second)
	if got, want := len(matches), 1; got != want {
		t.Fatalf("matches = %d, want %d", got, want)
	}
	if got, want := matches[0]["value"], int64(2); got != want {
		t.Fatalf("value = %d, want %d", got, want)
	}
}

func TestBucketMetadataUsesSaltAnd48BitHead(t *testing.T) {
	table := NewHashTable(1)
	key := IntKey(42)
	table.Insert(key, Row{"value": 42})

	metadata := table.buckets[table.bucketIndex(hashKey(key), 0)]
	if got, want := metadata.Salt(), saltFromHash(hashKey(key)); got != want {
		t.Fatalf("salt = %d, want %d", got, want)
	}
	if metadata.Head() == 0 {
		t.Fatal("head pointer is empty after insert")
	}
}

func TestBloomSaltMembership(t *testing.T) {
	metadata := newBucketMetadata(0b0111, 1)
	if !metadata.mayContainSalt(0b0011) {
		t.Fatal("filter rejected a salt whose bits are all present")
	}
	if metadata.mayContainSalt(0b1001) {
		t.Fatal("filter accepted a salt with an absent bit")
	}
}

func TestHashTableSeparatesBloomFalsePositives(t *testing.T) {
	// Find two distinct keys with the same initial bucket and Bloom salt.
	seen := make(map[uint64]IntKey)
	for key := IntKey(0); ; key++ {
		hash := hashKey(key)
		signature := uint64(saltFromHash(hash))<<6 | hash%64
		first, ok := seen[signature]
		if !ok {
			seen[signature] = key
			continue
		}
		table := NewHashTable(64)
		table.Insert(first, Row{"value": 1})
		if rows := table.Lookup(key); len(rows) != 0 {
			t.Fatal("Bloom false positive returned a row for a missing key")
		}
		table.Insert(key, Row{"value": 2})
		for wantedKey, value := range map[IntKey]int64{first: 1, key: 2} {
			rows := table.Lookup(wantedKey)
			if len(rows) != 1 || rows[0]["value"] != value {
				t.Fatalf("lookup(%d) = %v, want value %d", wantedKey, rows, value)
			}
		}
		return
	}
}

func TestBloomSaltSurvivesGrowth(t *testing.T) {
	table := NewHashTable(1)
	for key := IntKey(0); key < 256; key++ {
		table.Insert(key, Row{"value": int64(key)})
		table.Insert(key, Row{"value": int64(key)})
	}
	for key := IntKey(0); key < 256; key++ {
		rows := table.Lookup(key)
		if len(rows) != 2 {
			t.Fatalf("lookup(%d) returned %d rows, want 2", key, len(rows))
		}
		for _, row := range rows {
			if row["value"] != int64(key) {
				t.Fatalf("lookup(%d) returned %v", key, row)
			}
		}
	}
}

func sameInitialBucket(bucketCount int) (IntKey, IntKey) {
	seen := make(map[int]IntKey)
	for value := IntKey(0); ; value++ {
		index := int(hashKey(value) % uint64(bucketCount))
		if existing, ok := seen[index]; ok {
			return existing, value
		}
		seen[index] = value
	}
}
