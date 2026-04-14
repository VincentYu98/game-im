package service

import "testing"

func TestDirtyWordFilter_Replace(t *testing.T) {
	f := NewDirtyWordFilter([]string{"fuck", "shit"})

	tests := []struct {
		input   string
		wantOut string
	}{
		{"hello world", "hello world"},
		{"what the fuck", "what the ****"},
		{"FUCK you", "**** you"},
		{"FuCk off", "**** off"},
		{"shitty day", "****ty day"},
		{"holy shit and fuck", "holy **** and ****"},
		{"clean message", "clean message"},
	}

	for _, tt := range tests {
		r := f.Check(0, tt.input)
		if !r.Pass {
			t.Errorf("expected pass for %q", tt.input)
		}
		if r.Replace != tt.wantOut {
			t.Errorf("Check(%q).Replace = %q, want %q", tt.input, r.Replace, tt.wantOut)
		}
	}
}

func TestFilterChain_StopsOnReject(t *testing.T) {
	rejecter := &rejectFilter{reason: "blocked"}
	counter := &countFilter{}

	chain := NewFilterChain(rejecter, counter)
	r := chain.Check(1, "test")
	if r.Pass {
		t.Fatal("expected rejection")
	}
	if r.Reason != "blocked" {
		t.Fatalf("expected reason 'blocked', got %q", r.Reason)
	}
	if counter.calls != 0 {
		t.Fatal("second filter should not be called after rejection")
	}
}

func TestFilterChain_PropagatesReplacement(t *testing.T) {
	f1 := NewDirtyWordFilter([]string{"bad"})
	f2 := NewDirtyWordFilter([]string{"evil"})

	chain := NewFilterChain(f1, f2)
	r := chain.Check(0, "bad and evil")
	if !r.Pass {
		t.Fatal("expected pass")
	}
	if r.Replace != "*** and ****" {
		t.Fatalf("expected '*** and ****', got %q", r.Replace)
	}
}

func TestFilterChain_Empty(t *testing.T) {
	chain := NewFilterChain()
	r := chain.Check(0, "anything")
	if !r.Pass {
		t.Fatal("empty chain should pass")
	}
}

// Test helpers

type rejectFilter struct {
	reason string
}

func (f *rejectFilter) Check(_ int64, _ string) FilterResult {
	return FilterResult{Pass: false, Reason: f.reason}
}

type countFilter struct {
	calls int
}

func (f *countFilter) Check(_ int64, content string) FilterResult {
	f.calls++
	return FilterResult{Pass: true, Replace: content}
}
