package ca42admission

import "testing"

func TestAdmissionDurableOrderAndRecoveryMatrix(t *testing.T) {
	base := Observation{ClaimLive: true, BindingsExact: true, DurabilityUnambiguous: true}
	cases := []struct {
		name   string
		mutate func(*Observation)
		phase  Phase
		want   Action
	}{
		{name: "layout_to_nonce", phase: PhaseLayoutSwitched, want: ActionReserveNonce},
		{name: "nonce_to_journal_reserved", phase: PhaseNonceReserved, want: ActionPublishJournalReserved},
		{name: "journal_reserved_to_attempted", phase: PhaseJournalReserved, want: ActionPublishJournalAttempted},
		{name: "attempted_to_consumer", phase: PhaseJournalAttempted, mutate: func(o *Observation) { o.ConsumerOutcomeKnown = true }, want: ActionExecuteConsumer},
		{name: "attempted_response_lost_queries", phase: PhaseJournalAttempted, want: ActionQueryConsumer},
		{name: "attempted_query_still_unknown_recovers", phase: PhaseJournalAttempted, mutate: func(o *Observation) { o.ConsumerQueried = true }, want: ActionRecover},
		{name: "attempted_completed_to_verified", phase: PhaseJournalAttempted, mutate: func(o *Observation) { o.ConsumerOutcomeKnown, o.ConsumerCompleted = true, true }, want: ActionPublishJournalVerified},
		{name: "verified_to_nonce_commit", phase: PhaseJournalVerified, mutate: func(o *Observation) { o.ConsumerOutcomeKnown, o.ConsumerCompleted = true, true }, want: ActionCommitNonce},
		{name: "committed_is_done", phase: PhaseNonceCommitted, mutate: func(o *Observation) { o.ConsumerOutcomeKnown, o.ConsumerCompleted = true, true }, want: ActionFinishCommitted},
		{name: "recovery_is_terminal", phase: PhaseRecoveryRequired, want: ActionFinishRecovery},
		{name: "expired_before_attempt_recovers", phase: PhaseJournalReserved, mutate: func(o *Observation) { o.ClaimLive = false }, want: ActionRecover},
		{name: "binding_divergence_recovers", phase: PhaseJournalAttempted, mutate: func(o *Observation) { o.BindingsExact = false }, want: ActionRecover},
		{name: "ambiguous_durability_recovers", phase: PhaseNonceReserved, mutate: func(o *Observation) { o.DurabilityUnambiguous = false }, want: ActionRecover},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			observation := base
			observation.Phase = test.phase
			if test.mutate != nil {
				test.mutate(&observation)
			}
			got, err := Decide(observation)
			if err != nil || got != test.want {
				t.Fatalf("Decide() = %v, %v; want %v", got, err, test.want)
			}
		})
	}
}

func TestAdmissionContractRejectsContradictoryEvidence(t *testing.T) {
	cases := []Observation{
		{Phase: PhaseJournalAttempted, ClaimLive: true, BindingsExact: true, DurabilityUnambiguous: true, ConsumerQueried: true, ConsumerCompleted: true},
		{Phase: PhaseLayoutSwitched, ClaimLive: true, BindingsExact: true, DurabilityUnambiguous: true, ConsumerOutcomeKnown: true},
		{Phase: PhaseNonceReserved, ClaimLive: true, BindingsExact: true, DurabilityUnambiguous: true, ConsumerCompleted: true, ConsumerOutcomeKnown: true},
		{Phase: PhaseJournalReserved, ClaimLive: true, BindingsExact: true, DurabilityUnambiguous: true, ConsumerQueried: true},
		{Phase: PhaseJournalVerified, ClaimLive: true, BindingsExact: true, DurabilityUnambiguous: true},
		{Phase: PhaseNonceCommitted, ClaimLive: true, BindingsExact: true, DurabilityUnambiguous: true, ConsumerOutcomeKnown: true},
	}
	for index, observation := range cases {
		if action, err := Decide(observation); err == nil {
			t.Fatalf("contradictory observation %d accepted with action %v", index, action)
		}
	}
}

func TestAdmissionContractRejectsInvalidAndNeverBlindlyRetriesUnknownConsumer(t *testing.T) {
	if _, err := Decide(Observation{}); err == nil {
		t.Fatal("invalid zero observation accepted")
	}
	action, err := Decide(Observation{
		Phase: PhaseJournalAttempted, ClaimLive: true, BindingsExact: true,
		ConsumerOutcomeKnown: false, DurabilityUnambiguous: true,
	})
	if err != nil || action != ActionQueryConsumer {
		t.Fatalf("unknown consumer outcome = %v, %v; want query", action, err)
	}
}
