package engine

import (
	"errors"
	"testing"
)

func TestChooseSemanticMatch(t *testing.T) {
	if _, err := chooseSemanticMatch(0, 0); !errors.Is(err, ErrStaleRef) {
		t.Fatalf("empty match: %v", err)
	}
	idx, err := chooseSemanticMatch(1, 5)
	if err != nil || idx != 0 {
		t.Fatalf("unique match: idx=%d err=%v", idx, err)
	}
	idx, err = chooseSemanticMatch(3, 2)
	if err != nil || idx != 2 {
		t.Fatalf("nth match: idx=%d err=%v", idx, err)
	}
	if _, err := chooseSemanticMatch(2, 5); !errors.Is(err, ErrAmbiguousRef) {
		t.Fatalf("ambiguous: %v", err)
	}
}

func TestNewRuntimeWrapsOwnership(t *testing.T) {
	b := &Browser{ownership: OwnershipAttached, timeout: DefaultActionTimeout}
	rt := NewRuntime(b)
	if rt.Ownership != OwnershipAttached {
		t.Fatalf("ownership=%s", rt.Ownership)
	}
	if rt.Generation != 1 {
		t.Fatalf("generation=%d", rt.Generation)
	}
}

func TestIsPermanentActionError(t *testing.T) {
	if !isPermanentActionError(ErrStaleRef) {
		t.Fatal("stale should be permanent")
	}
	if !isPermanentActionError(errors.New("element is detached")) {
		t.Fatal("detached should be permanent")
	}
	if isPermanentActionError(errors.New("element is not visible")) {
		t.Fatal("not visible should poll")
	}
}
