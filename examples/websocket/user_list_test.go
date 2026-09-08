package main

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestUserListContents(t *testing.T) {
	for _, names := range [][]string{nil, {"用户1"}, {"用户1", "Alice", "你好🌏"}} {
		room := &ChatRoom{users: make(map[string]*chatClient)}
		for _, name := range names {
			room.users[name] = nil
		}
		list, count := room.userList()
		if count != len(names) || !strings.HasPrefix(list, "在线用户: ") {
			t.Fatalf("list=%q, count=%d", list, count)
		}
		body := strings.TrimPrefix(list, "在线用户: ")
		if len(names) == 0 {
			if body != "" {
				t.Fatalf("empty list=%q", list)
			}
			continue
		}
		actual, expected := strings.Split(body, ", "), slices.Clone(names)
		slices.Sort(actual)
		slices.Sort(expected)
		if !slices.Equal(actual, expected) {
			t.Fatalf("list names=%v, want %v", actual, expected)
		}
	}
}

func TestUserListSnapshotDuringMembershipChanges(t *testing.T) {
	room := &ChatRoom{users: make(map[string]*chatClient)}
	var updates sync.WaitGroup
	updates.Go(func() {
		for i := 0; i < 1000; i++ {
			room.mu.Lock()
			room.users[fmt.Sprint(i)] = nil
			delete(room.users, fmt.Sprint(i-10))
			room.mu.Unlock()
		}
	})
	defer updates.Wait()
	for i := 0; i < 1000; i++ {
		list, count := room.userList()
		body := strings.TrimPrefix(list, "在线用户: ")
		if count == 0 {
			if body != "" {
				t.Fatal("empty snapshot contained usernames")
			}
			continue
		}
		if names := strings.Split(body, ", "); len(names) != count {
			t.Fatalf("snapshot contains %d usernames but reports %d", len(names), count)
		}
	}
}

func BenchmarkUserList(b *testing.B) {
	for _, count := range []int{10, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			room := &ChatRoom{users: make(map[string]*chatClient, count)}
			for i := 0; i < count; i++ {
				room.users[fmt.Sprintf("用户%d", i)] = nil
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = room.userList()
			}
		})
	}
}
