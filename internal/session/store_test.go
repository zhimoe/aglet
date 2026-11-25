package session

import (
	"context"
	"path/filepath"
	"testing"

	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func TestListPreview(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "sessions.db"), "aglet", "local")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	id1, err := store.Ensure(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Ensure(ctx, "s2"); err != nil {
		t.Fatal(err)
	}
	sess, err := store.Get(ctx, id1)
	if err != nil {
		t.Fatal(err)
	}
	ev := session.NewEvent(ctx, "inv-1")
	ev.Author = "user"
	ev.Content = genai.NewContentFromText("hello preview text", genai.RoleUser)
	if err := store.Service.AppendEvent(ctx, sess, ev); err != nil {
		t.Fatal(err)
	}
	infos, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("infos = %#v", infos)
	}
	found := false
	for _, in := range infos {
		if in.ID == id1 {
			found = true
			if in.Events != 1 || in.Preview != "hello preview text" {
				t.Fatalf("info = %#v", in)
			}
		}
	}
	if !found {
		t.Fatalf("session %s missing: %#v", id1, infos)
	}
}
