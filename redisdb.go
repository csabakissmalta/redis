package redis

import (
	"sync"
	"time"
)

const (
	keysMapSize           = 32
	redisDbMapSizeDefault = 3
)

// A redis database.
// There can be more than one in a redis instance.
type RedisDb struct {
	// Database id
	id DatabaseId

	// All keys in this db.
	keys Keys

	// Keys with a timeout set.
	expiringKeys Keys

	// TODO long long avg_ttl;          /* Average TTL, just for stats */

	redis *Redis
}

// Redis databases map
type RedisDbs map[DatabaseId]*RedisDb

// Database id
type DatabaseId uint

// Key-Item map
type Keys map[string]Item

// The item interface. An item is the value of a key.
type Item interface {
	// The pointer to the value.
	Value() interface{}

	// The id of the type of the Item.
	// This need to be constant for the type because it is
	// used when de-/serializing item from/to disk.
	ValueType() uint64
	// The type of the Item as string.
	ValueTypeFancy() string

	// GetCommand timestamp when the item expires.
	Expiry() time.Time
	// Expiry is set.
	Expires() bool

	// OnDelete is triggered before the key of the item is deleted.
	// db is the affected database.
	OnDelete(key *string, db *RedisDb)
}

// NewRedisDb creates a new db.
func NewRedisDb(id DatabaseId, r *Redis) *RedisDb {
	return &RedisDb{
		id:           id,
		redis:        r,
		keys:         make(Keys, keysMapSize),
		expiringKeys: make(Keys, keysMapSize),
	}
}

// RedisDb gets the redis database by its id or creates and returns it if not exists.
func (r *Redis) RedisDb(dbId DatabaseId) *RedisDb {
	getDb := func() *RedisDb { // returns nil if db not exists
		if db, ok := r.redisDbs[dbId]; ok {
			return db
		}
		return nil
	}

	r.Mu().RLock()
	db := getDb()
	r.Mu().RUnlock()
	if db != nil {
		return db
	}

	// create db
	r.Mu().Lock()
	defer r.Mu().Unlock()
	// check if db does not exists again since
	// multiple "mutex readers" can come to this point
	db = getDb()
	if db != nil {
		return db
	}
	// now really create db of that id
	r.redisDbs[dbId] = NewRedisDb(dbId, r)
	return r.redisDbs[dbId]
}

func (r *Redis) RedisDbs() RedisDbs {
	r.Mu().RLock()
	defer r.Mu().RUnlock()
	return r.redisDbs
}

// Redis gets the redis instance.
func (db *RedisDb) Redis() *Redis {
	return db.redis
}

// Mu gets the mutex.
func (db *RedisDb) Mu() *sync.RWMutex {
	return db.Redis().Mu()
}

// Id gets the db id.
func (db *RedisDb) Id() DatabaseId {
	return db.id
}

// IsEmpty checks db is empty.
func (db *RedisDb) IsEmpty() bool {
	db.Mu().RLock()
	defer db.Mu().RUnlock()
	return len(db.keys) == 0
}

// IsEmptyExpire checks db has any expiring keys.
func (db *RedisDb) IsEmptyExpire() bool {
	db.Mu().RLock()
	defer db.Mu().RUnlock()
	return len(db.expiringKeys) == 0
}

// Keys gets all keys in this db.
func (db *RedisDb) Keys() Keys {
	db.Mu().RLock()
	defer db.Mu().RUnlock()
	return db.keys
}

// ExpiringKeys gets keys with an expiry set.
func (db *RedisDb) ExpiringKeys() Keys {
	db.Mu().RLock()
	defer db.Mu().RUnlock()
	return db.expiringKeys
}

// Sets a key with an item.
func (db *RedisDb) Set(key *string, i Item) {
	db.Mu().Lock()
	defer db.Mu().Unlock()
	db.keys[*key] = i
	if i.Expires() {
		db.expiringKeys[*key] = i
	}
}

// Returns the item by the key or nil if key does not exists.
func (db *RedisDb) Get(key *string) Item {
	db.Mu().RLock()
	defer db.Mu().RUnlock()
	return db.get(key)
}

func (db *RedisDb) get(key *string) Item {
	i, _ := db.keys[*key]
	return i
}

// Deletes a key, returns true if key existed.
func (db *RedisDb) Delete(keys ...*string) int64 {
	// TODO if it makes a difference, check keys exists with RLock and then if exists RWLock
	db.Mu().Lock()
	defer db.Mu().Unlock()
	var c int64
	for _, k := range keys {
		if k != nil && db.delete(k) {
			c++
		}
	}
	return c
}

// If checkExists is false, then return bool is reprehensible.
func (db *RedisDb) delete(key *string) bool {
	i := db.get(key)
	if i == nil {
		return false
	}
	i.OnDelete(key, db)
	delete(db.keys, *key)
	delete(db.expiringKeys, *key)
	return true
}

// Check if key exists.
func (db *RedisDb) Exists(key *string) bool {
	db.Mu().RLock()
	defer db.Mu().RUnlock()
	return db.exists(key)
}
func (db *RedisDb) exists(key *string) bool {
	_, ok := db.keys[*key]
	return ok
}

// Check if key can expire.
func (db *RedisDb) Expires(key *string) bool {
	db.Mu().RLock()
	defer db.Mu().RUnlock()
	_, ok := db.expiringKeys[*key]
	return ok
}

// GetOrExpire gets the item or nil if expired or not exists.
func (db *RedisDb) GetOrExpired(key *string, deleteIfExpired bool) Item {
	// TODO mutex optimize this func so that a RLock is mainly first opened

	db.Mu().Lock()
	defer db.Mu().Unlock()
	i, ok := db.keys[*key]
	if !ok {
		return nil
	}
	if ItemExpired(i) {
		if deleteIfExpired {
			db.delete(key)
		}
		return nil
	}
	return i
}

// Expired check if a timestamp is expired.
func Expired(expireAt time.Time) bool {
	return time.Now().After(expireAt)
}

// ItemExpired check if an item can and is expired
func ItemExpired(i Item) bool {
	return i.Expires() && Expired(i.Expiry())
}
