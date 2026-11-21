package objects

// The game-object name cache: per-entry query answers fill World.Name for doors, chests and gossip objects, and
// UnknownGameObjects feeds the session's one-query-per-entry round.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// gameObjectAt puts one game object of entry at the origin of a fresh world.
func gameObjectAt(w *World, guid wire.GUID, entry int32) {
	o := &Object{GUID: guid, Type: TypeGameObject, Info: Info{Entry: entry, HasGameObject: true}}
	w.objects[guid] = o
}

// TestGameObjectNameCache: a cached query answer names the object; without one the name is empty but the entry
// still lists as unknown; after caching it no longer does.
func TestGameObjectNameCache(t *testing.T) {
	w := NewWorld(0)
	guid := wire.GUID{Low: 7, High: 0xF100000000000000}
	gameObjectAt(w, guid, 17153)
	o, ok := w.Object(guid)
	if !ok {
		t.Fatal("no object")
	}
	if w.Name(o) != "" {
		t.Fatalf("unnamed gameobject has name %q", w.Name(o))
	}
	if got := w.UnknownGameObjects(0, 0, 0, 10); len(got) != 1 || got[0] != 17153 {
		t.Fatalf("unknown %+v", got)
	}
	g := GameObjectName{Entry: 17153, Found: true, Type: 0, DisplayID: 1369}
	g.Name[0] = "Gate"
	w.SetGameObjectName(g)
	o, _ = w.Object(guid)
	if w.Name(o) != "Gate" {
		t.Fatalf("name %q", w.Name(o))
	}
	if got, ok := w.GameObjectName(17153); !ok || got.Type != 0 || got.DisplayID != 1369 {
		t.Fatalf("cache %+v %v", got, ok)
	}
	if got := w.UnknownGameObjects(0, 0, 0, 10); len(got) != 0 {
		t.Fatalf("known listed %+v", got)
	}
	if _, ok := w.GameObjectName(1); ok {
		t.Fatal("unknown entry cached")
	}
	// A non-gameobject is never listed as an unknown gameobject.
	w2 := NewWorld(0)
	w2.objects[guid] = &Object{GUID: guid, Type: TypeUnit, Info: Info{Entry: 5, HasUnit: true}}
	if got := w2.UnknownGameObjects(0, 0, 0, 10); len(got) != 0 {
		t.Fatalf("unit listed %+v", got)
	}
}
