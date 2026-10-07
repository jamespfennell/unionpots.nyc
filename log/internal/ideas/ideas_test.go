package ideas

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	got := parse(`# comment
3f2a9c1e0b7d4a5f8e6c2b1a0d9f8e7c  studio filter on the finished page

  0123456789abcdef0123456789abcdef
not-a-hash  ignored
0123456789ABCDEF0123456789ABCDEF uppercase is not ours
`)
	want := []string{"3f2a9c1e0b7d4a5f8e6c2b1a0d9f8e7c", "0123456789abcdef0123456789abcdef"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parse = %v, want %v", got, want)
	}
	if Done() == nil && doneFile == "" {
		t.Errorf("done.txt should be embedded")
	}
}
