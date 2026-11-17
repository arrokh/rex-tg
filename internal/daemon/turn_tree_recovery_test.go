package daemon

import (
	"testing"

	"rex-tg/internal/config"
)

func TestTurnStartSnapshotSurvivesPromptReplay(t *testing.T) {
	cfg := config.Default(t.TempDir())
	cfg.MiniApp.URL = "https://synthetic.example"
	original := turnTree{top: t.TempDir(), tree: "original-tree", key: "prompt-event"}
	d := &daemon{ctx: t.Context(), cfg: cfg, turnTrees: map[int64]turnTree{1: original}}
	d.miniUp.Store(true)
	d.turnStarted(1, original.top, "prompt-event", false) // rollback restored a non-running tracker
	d.wg.Wait()
	if got := d.turnTrees[1]; got != original {
		t.Fatalf("replayed prompt replaced the original turn start: %+v", got)
	}
	d.onTurnTree(evTurnTree{conv: 1, t: turnTree{top: original.top, tree: "stale-tree", key: "previous-prompt"}})
	if got := d.turnTrees[1]; got != original {
		t.Fatalf("a stale snapshot worker replaced the current turn: %+v", got)
	}
}

func TestTurnAnswerKeepsStartUntilTranscriptCommit(t *testing.T) {
	start := turnTree{key: "prompt-event"} // start snapshot still in flight
	d := &daemon{turnTrees: map[int64]turnTree{1: start}}
	d.turnAnswered(1, 2, "answer-event")
	if got, ok := d.turnTrees[1]; !ok || got.key != start.key || !got.answered {
		t.Fatal("an uncommitted answer discarded the start needed for replay")
	}
	d.onTurnTree(evTurnTree{conv: 1, t: turnTree{tree: "finished-start", key: start.key}})
	if !d.turnTrees[1].answered {
		t.Fatal("a snapshot worker cleared the answered marker")
	}
}

func TestNewPromptAfterAnAnswerGetsANewTurnStart(t *testing.T) {
	cfg := config.Default(t.TempDir())
	cfg.MiniApp.URL = "https://synthetic.example"
	d := &daemon{ctx: t.Context(), cfg: cfg, turnTrees: map[int64]turnTree{
		1: {key: "old-prompt", tree: "old-start", answered: true},
	}}
	d.miniUp.Store(true)
	d.turnStarted(1, t.TempDir(), "new-prompt", true)
	d.wg.Wait()
	if got := d.turnTrees[1]; got.key != "new-prompt" || got.answered || got.tree == "old-start" {
		t.Fatalf("new prompt inherited the previous answered turn: %+v", got)
	}
}
