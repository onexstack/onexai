package llmtrain

import (
	"fmt"

	"github.com/looplab/fsm"

	"github.com/onexstack/onexai/internal/jobserver/model"
	fsmutil "github.com/onexstack/onexai/internal/pkg/util/fsm"
	known "github.com/onexstack/onexai/pkg/known/job"
)

// StateMachine represents a finite state machine for managing daily estimation jobs.
type StateMachine struct {
	// Watcher observes changes or events related to the job.
	Watcher *Watcher

	// Job holds the metadata and status of the current LLM training job.
	Job *model.JobM

	// FSM is the underlying finite state machine instance.
	FSM *fsm.FSM
}

// NewStateMachine initializes a new StateMachine instance.
// It sets up the FSM events, transitions, and callbacks for the LLM training lifecycle.
func NewStateMachine(initial string, watcher *Watcher, job *model.JobM) *StateMachine {
	sm := &StateMachine{
		Watcher: watcher,
		Job:     job,
	}

	sm.FSM = fsm.NewFSM(
		initial,
		fsm.Events{
			// Define state transitions for the daily estimation process.
			{Name: known.LLMTrainPending, Src: []string{known.LLMTrainPending}, Dst: known.LLMTrainDownloading},
			{Name: known.LLMTrainDownloading, Src: []string{known.LLMTrainDownloading}, Dst: known.LLMTrainDownloaded},
			{Name: known.LLMTrainDownloaded, Src: []string{known.LLMTrainDownloaded}, Dst: known.LLMTrainEmbedding},
			{Name: known.LLMTrainEmbedding, Src: []string{known.LLMTrainEmbedding}, Dst: known.LLMTrainEmbedded},
			{Name: known.LLMTrainEmbedded, Src: []string{known.LLMTrainEmbedded}, Dst: known.LLMTrainTraining},
			{Name: known.LLMTrainTraining, Src: []string{known.LLMTrainTraining}, Dst: known.LLMTrainTrained},
			{Name: known.LLMTrainTrained, Src: []string{known.LLMTrainTrained}, Dst: known.LLMTrainSucceeded},
		},
		fsm.Callbacks{
			// enter_state executes before specific enter_xxx events.
			// Context: event=Pending, current=Downloading.
			"enter_state": fsmutil.WrapEvent(sm.EnterState),

			// Context: event=Downloading, current=Downloaded.
			fmt.Sprintf("enter_%s", known.LLMTrainDownloaded): fsmutil.WrapEvent(sm.Download),

			fmt.Sprintf("enter_%s", known.LLMTrainEmbedded): fsmutil.WrapEvent(sm.Embedding),

			fmt.Sprintf("enter_%s", known.LLMTrainTrained): fsmutil.WrapEvent(sm.Train),
		},
	)

	return sm
}
