package ca42admission

import "errors"

// Phase is a diagnostic projection of the durable cross-store admission
// boundary. A Phase grants no mutation authority; the production coordinator
// must derive it from retained nonce, Journal, ledger, inventory and consumer
// capabilities.
type Phase uint8

const (
	PhaseLayoutSwitched Phase = iota + 1
	PhaseNonceReserved
	PhaseJournalReserved
	PhaseJournalAttempted
	PhaseJournalVerified
	PhaseNonceCommitted
	PhaseRecoveryRequired
)

// Action is the sole legal next durable action after the coordinator has
// revalidated every retained capability. It is not an authorization token.
type Action uint8

const (
	ActionReserveNonce Action = iota + 1
	ActionPublishJournalReserved
	ActionPublishJournalAttempted
	ActionExecuteConsumer
	ActionQueryConsumer
	ActionPublishJournalVerified
	ActionCommitNonce
	ActionFinishCommitted
	ActionFinishRecovery
	ActionRecover
)

// Observation is intentionally diagnostic and forgeable. Production code may
// use Decide only after it has independently reconstructed this observation
// from opaque, retained capabilities. Callers must never supply it to an
// admission entry point.
type Observation struct {
	Phase                 Phase
	ClaimLive             bool
	BindingsExact         bool
	ConsumerQueried       bool
	ConsumerOutcomeKnown  bool
	ConsumerCompleted     bool
	DurabilityUnambiguous bool
}

// Decide freezes the monotonic recovery matrix shared by the future nonce,
// Journal and consumer adapters. It never mutates state.
func Decide(observation Observation) (Action, error) {
	if observation.Phase < PhaseLayoutSwitched || observation.Phase > PhaseRecoveryRequired {
		return 0, errors.New("CA42 admission phase invalid")
	}
	if err := validateObservation(observation); err != nil {
		return 0, err
	}
	if observation.Phase == PhaseRecoveryRequired {
		return ActionFinishRecovery, nil
	}
	if !observation.BindingsExact || !observation.DurabilityUnambiguous {
		return ActionRecover, nil
	}
	switch observation.Phase {
	case PhaseLayoutSwitched:
		if !observation.ClaimLive {
			return ActionRecover, nil
		}
		return ActionReserveNonce, nil
	case PhaseNonceReserved:
		if !observation.ClaimLive {
			return ActionRecover, nil
		}
		return ActionPublishJournalReserved, nil
	case PhaseJournalReserved:
		if !observation.ClaimLive {
			return ActionRecover, nil
		}
		return ActionPublishJournalAttempted, nil
	case PhaseJournalAttempted:
		if observation.ConsumerCompleted {
			return ActionPublishJournalVerified, nil
		}
		if !observation.ConsumerOutcomeKnown {
			if observation.ConsumerQueried {
				return ActionRecover, nil
			}
			return ActionQueryConsumer, nil
		}
		if observation.ConsumerOutcomeKnown {
			if !observation.ClaimLive {
				return ActionRecover, nil
			}
			return ActionExecuteConsumer, nil
		}
		return ActionRecover, nil
	case PhaseJournalVerified:
		return ActionCommitNonce, nil
	case PhaseNonceCommitted:
		return ActionFinishCommitted, nil
	default:
		return 0, errors.New("CA42 admission phase invalid")
	}
}

func validateObservation(observation Observation) error {
	if observation.ConsumerCompleted && !observation.ConsumerOutcomeKnown {
		return errors.New("CA42 admission completed consumer outcome is not known")
	}
	switch observation.Phase {
	case PhaseLayoutSwitched, PhaseNonceReserved, PhaseJournalReserved:
		if observation.ConsumerQueried || observation.ConsumerOutcomeKnown || observation.ConsumerCompleted {
			return errors.New("CA42 admission consumer evidence precedes attempted boundary")
		}
	case PhaseJournalVerified, PhaseNonceCommitted:
		if !observation.ConsumerOutcomeKnown || !observation.ConsumerCompleted {
			return errors.New("CA42 admission verified boundary lacks completed consumer evidence")
		}
	}
	return nil
}
