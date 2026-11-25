package plan

import "testing"

func TestBoardUpdate(t *testing.T) {
	b := New()
	var got []Step
	b.SetEmitter(func(steps []Step) { got = steps })
	if err := b.Update([]Step{{Step: "one", Status: StatusInProgress}, {Step: "two"}}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Status != StatusPending {
		t.Fatalf("emitted = %#v", got)
	}
	got[0].Step = "mutated"
	if b.Steps()[0].Step != "one" {
		t.Fatal("emitter should receive a copy")
	}
	if err := b.Update([]Step{{Step: "bad", Status: "nope"}}); err == nil {
		t.Fatal("expected status error")
	}
	if err := b.Update(nil); err == nil {
		t.Fatal("expected empty plan error")
	}
}
