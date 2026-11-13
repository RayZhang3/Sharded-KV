package shardkv

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"6.824/labgob"
	"6.824/raft"
	"6.824/shardctrler"
)

// The destination group has stored the shard imports in its logs, but its
// snapshots still mark the shards as PULLING. With the old group offline,
// recovery must apply those imports without any client requests.
func TestShardRecoveryAfterRestart(t *testing.T) {
	cfg := make_config(t, 3, false, -1)
	defer cfg.cleanup()
	cfg.join(1)
	previous := cfg.mck.Query(-1)
	cfg.join(0)
	current := cfg.mck.Query(-1)
	for gi := 0; gi < cfg.ngroups; gi++ {
		cfg.ShutdownGroup(gi)
	}

	gid := cfg.groups[0].gid
	const snapshotIndex = 100 // Earlier configuration entries are already snapshotted.
	fixture := &ShardKV{
		gid: gid, config: current, prevConfig: previous,
		currentState: make(map[string]string), clientSeq: make(map[int64]int),
	}
	entries := []raft.LogEntry{{}}
	expected := make(map[string]string)
	for shard := 0; shard < shardctrler.NShards; shard++ {
		fixture.shardsState[shard] = NO_SERVING
		if current.Shards[shard] != gid {
			continue
		}
		fixture.shardsState[shard] = PULLING
		key := string(rune(100 + shard)) // key2shard(key) == shard
		value := fmt.Sprintf("persisted-shard-%d", shard)
		expected[key] = value
		command := CommandMsg{ShardRequestOperation, ShardRequestMsg{
			ConfigNum: current.Num, ShardIndex: shard, GID: gid,
			ShardData: map[string]string{key: value}, ClientSeqNum: map[int64]int{},
		}}
		entries = append(entries, raft.LogEntry{Term: 1, Index: snapshotIndex + len(entries), Command: command})
	}
	if len(expected) == 0 {
		t.Fatal("fixture has no incoming shards")
	}
	var state bytes.Buffer
	encoder := labgob.NewEncoder(&state)
	// Same persistent format as raft.getStatePersistData: term, vote, log,
	// snapshot index, snapshot term. Volatile commitIndex is lost on restart.
	for _, value := range []interface{}{1, -1, entries, snapshotIndex, 1} {
		if err := encoder.Encode(value); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := fixture.getServerPersistData()
	for i := 0; i < cfg.n; i++ {
		persister := raft.MakePersister()
		persister.SaveStateAndSnapshot(state.Bytes(), snapshot)
		cfg.groups[0].saved[i] = persister
	}
	cfg.StartGroup(0)

	recovered := func() bool {
		for _, kv := range cfg.groups[0].servers {
			kv.mu.Lock()
			ready := true
			for key, value := range expected {
				if kv.shardsState[key2shard(key)] != SERVING || kv.currentState[key] != value {
					ready = false
				}
			}
			kv.mu.Unlock()
			if !ready {
				return false
			}
		}
		return true
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if recovered() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("destination replicas did not recover the persisted shards after restart")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
