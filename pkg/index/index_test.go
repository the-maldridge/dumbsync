package index

import "testing"

func TestComputeDifference(t *testing.T) {
	local := Index{Files: map[string][]byte{
		"a": []byte("deadbeef"),
		"b": []byte("deadbeef"),
		"c": []byte("deadbeef"),
		"e": []byte("deadbeef"),
	}}
	remote := Index{Files: map[string][]byte{
		"a": []byte("caffeeee"),
		"c": []byte("deadbeef"),
		"d": []byte("deadbeef"),
		"e": []byte("deadbeef"),
	}}
	added, removed, changed := local.computeDifference(&remote)
	if len(added) != 1 || len(removed) != 1 || len(changed) != 1 {
		t.Fatal()
	}
	if added[0] != "d" {
		t.Fatal()
	}
	if removed[0] != "b" {
		t.Fatal()
	}
	if changed[0] != "a" {
		t.Fatal()
	}
}
