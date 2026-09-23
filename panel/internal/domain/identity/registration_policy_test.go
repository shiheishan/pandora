package identity

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type registrationPolicyRow struct {
	value any
	err   error
}

func (r registrationPolicyRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 1 {
		return errors.New("unexpected registration-policy scan destination count")
	}
	switch out := dest[0].(type) {
	case *string:
		*out = r.value.(string)
	case *bool:
		*out = r.value.(bool)
	default:
		return errors.New("unexpected registration-policy scan destination")
	}
	return nil
}

type registrationPolicyTx struct {
	pgx.Tx
	mode        string
	modeErr     error
	featureOff  bool
	featureErr  error
	inviteID    string
	inviteErr   error
	emailVerify bool
	emailErr    error
	queries     []string
	queryArgs   [][]any
	execs       int
}

func (tx *registrationPolicyTx) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	tx.queries = append(tx.queries, query)
	tx.queryArgs = append(tx.queryArgs, args)
	switch {
	case strings.Contains(query, "FROM feature_switches"):
		if tx.featureErr != nil {
			return registrationPolicyRow{err: tx.featureErr}
		}
		return registrationPolicyRow{value: !tx.featureOff}
	case strings.Contains(query, "auth.registration_mode"):
		if tx.modeErr != nil {
			return registrationPolicyRow{err: tx.modeErr}
		}
		return registrationPolicyRow{value: tx.mode}
	case strings.Contains(query, "FROM invite_codes"):
		if tx.inviteErr != nil {
			return registrationPolicyRow{err: tx.inviteErr}
		}
		return registrationPolicyRow{value: tx.inviteID}
	case strings.Contains(query, "auth.email_verification"):
		if tx.emailErr != nil {
			return registrationPolicyRow{err: tx.emailErr}
		}
		return registrationPolicyRow{value: tx.emailVerify}
	default:
		return registrationPolicyRow{err: errors.New("unexpected registration-policy query")}
	}
}

func (tx *registrationPolicyTx) Exec(
	_ context.Context, _ string, _ ...any,
) (pgconn.CommandTag, error) {
	tx.execs++
	return pgconn.CommandTag{}, errors.New("registration policy must refuse before writes")
}

type registrationPolicyRunner struct {
	tx    *registrationPolicyTx
	calls int
	scope db.Scope
}

type boundInviteTestRow struct {
	err error
}

func (r boundInviteTestRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 4 {
		return errors.New("unexpected bound-invite scan destination count")
	}
	owner := testUserID
	channel := "partner"
	campaign := "launch"
	*(dest[0].(*string)) = "11111111-1111-1111-1111-111111111111"
	*(dest[1].(**string)) = &owner
	*(dest[2].(**string)) = &channel
	*(dest[3].(**string)) = &campaign
	return nil
}

type boundInviteTestTx struct {
	pgx.Tx
	query     string
	queryArgs []any
	execs     []string
}

func (tx *boundInviteTestTx) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	tx.query = query
	tx.queryArgs = args
	return boundInviteTestRow{}
}

func (tx *boundInviteTestTx) Exec(
	_ context.Context, query string, _ ...any,
) (pgconn.CommandTag, error) {
	tx.execs = append(tx.execs, query)
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (r *registrationPolicyRunner) InTx(
	ctx context.Context, scope db.Scope, fn func(pgx.Tx) error,
) error {
	r.calls++
	r.scope = scope
	return fn(r.tx)
}

func TestRegistrationModeDefaultsClosedAndRejectsUnknownValue(t *testing.T) {
	ctx := context.Background()
	tx := &registrationPolicyTx{modeErr: pgx.ErrNoRows}
	mode, err := loadRegistrationMode(ctx, tx, testTenantID)
	if err != nil || mode != RegistrationModeClosed {
		t.Fatalf("missing registration setting mode=%q err=%v, want closed", mode, err)
	}

	tx = &registrationPolicyTx{mode: "unexpected"}
	if _, err := loadRegistrationMode(ctx, tx, testTenantID); err == nil {
		t.Fatal("unknown registration mode must fail closed")
	}
}

func TestRegistrationFeatureSwitchDisabledOverridesOpen(t *testing.T) {
	tx := &registrationPolicyTx{featureOff: true, mode: RegistrationModeOpen}
	mode, err := loadRegistrationMode(context.Background(), tx, testTenantID)
	if err != nil || mode != RegistrationModeClosed {
		t.Fatalf("disabled feature mode=%q err=%v, want closed", mode, err)
	}
	if len(tx.queries) != 1 || !strings.Contains(tx.queries[0], "feature_switches") {
		t.Fatalf("disabled feature must stop before mode query: %#v", tx.queries)
	}
}

func TestRegistrationStartPolicyModeMatrix(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name       string
		tx         *registrationPolicyTx
		inviteCode string
		wantMode   string
		wantDenied bool
	}{
		{"closed", &registrationPolicyTx{mode: RegistrationModeClosed}, "", RegistrationModeClosed, true},
		{"open", &registrationPolicyTx{mode: RegistrationModeOpen}, "", RegistrationModeOpen, false},
		{"open invalid optional invite", &registrationPolicyTx{mode: RegistrationModeOpen, inviteErr: pgx.ErrNoRows}, "bad", RegistrationModeOpen, true},
		{"invite missing", &registrationPolicyTx{mode: RegistrationModeInviteOnly}, "", RegistrationModeInviteOnly, true},
		{"invite invalid", &registrationPolicyTx{mode: RegistrationModeInviteOnly, inviteErr: pgx.ErrNoRows}, "bad", RegistrationModeInviteOnly, true},
		{"invite valid", &registrationPolicyTx{mode: RegistrationModeInviteOnly, inviteID: testUserID}, " valid8 ", RegistrationModeInviteOnly, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, inviteID, err := enforceRegistrationStartPolicy(ctx, tt.tx, testTenantID, tt.inviteCode)
			if mode != tt.wantMode {
				t.Fatalf("mode=%q, want %q", mode, tt.wantMode)
			}
			if tt.wantDenied {
				assertHTTPErrorCode(t, err, httpx.CodeForbidden)
			} else if err != nil {
				t.Fatalf("unexpected policy error: %v", err)
			}
			if tt.name == "invite valid" {
				if inviteID != testUserID {
					t.Fatalf("invite id=%q, want %q", inviteID, testUserID)
				}
				got := tt.tx.queryArgs[len(tt.tx.queryArgs)-1]
				if len(got) != 2 || got[1] != "VALID8" {
					t.Fatalf("invite lookup args=%#v, want normalized code", got)
				}
			}
		})
	}
}

func TestRegistrationCompletePolicyRechecksCurrentMode(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		name, mode, invite string
		denied             bool
	}{
		{"closed after start", RegistrationModeClosed, "VALID8", true},
		{"invite binding checked after session lock", RegistrationModeInviteOnly, "", false},
		{"open", RegistrationModeOpen, "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tx := &registrationPolicyTx{mode: tt.mode}
			mode, err := enforceRegistrationCompletePolicy(ctx, tx, testTenantID)
			if mode != tt.mode {
				t.Fatalf("mode=%q, want %q", mode, tt.mode)
			}
			if tt.denied {
				assertHTTPErrorCode(t, err, httpx.CodeForbidden)
			} else if err != nil {
				t.Fatalf("unexpected policy error: %v", err)
			}
		})
	}
}

func TestClosedRegistrationServiceRefusesBeforeSessionOrUserAccess(t *testing.T) {
	for _, complete := range []bool{false, true} {
		name := "start"
		if complete {
			name = "complete"
		}
		t.Run(name, func(t *testing.T) {
			tx := &registrationPolicyTx{mode: RegistrationModeClosed}
			runner := &registrationPolicyRunner{tx: tx}
			svc := &Service{pool: runner}
			var err error
			if complete {
				_, err = svc.CompleteRegistration(context.Background(), testTenantID,
					CompleteRegistrationInput{RegistrationToken: "unused", Password: "Aa123456"})
			} else {
				_, err = svc.StartRegistration(context.Background(), testTenantID,
					StartRegistrationInput{Email: "blocked@example.com"})
			}
			assertHTTPErrorCode(t, err, httpx.CodeForbidden)
			if runner.calls != 1 || tx.execs != 0 || len(tx.queries) != 2 {
				t.Fatalf("closed registration calls=%d queries=%d execs=%d, want 1/2/0",
					runner.calls, len(tx.queries), tx.execs)
			}
			if !strings.Contains(tx.queries[0], "feature_switches") ||
				!strings.Contains(tx.queries[1], "auth.registration_mode") {
				t.Fatalf("policy query order incorrect: %#v", tx.queries)
			}
		})
	}
}

func TestRegistrationPolicyReadFailureDoesNotReturnOpenDefaults(t *testing.T) {
	tx := &registrationPolicyTx{modeErr: errors.New("database unavailable")}
	runner := &registrationPolicyRunner{tx: tx}
	svc := &Service{pool: runner}
	policy, err := svc.RegistrationPolicy(context.Background(), testTenantID)
	if err == nil {
		t.Fatal("site policy read failure must be returned")
	}
	if policy.Mode != RegistrationModeClosed || !policy.EmailVerification {
		t.Fatalf("failure defaults=%+v, want closed and verification=true", policy)
	}
}

func TestBoundInviteUsesPersistedIdentityDatabaseClockAndStrictReferralInsert(t *testing.T) {
	tx := &boundInviteTestTx{}
	invite, err := lockBoundInvite(context.Background(), tx, testTenantID,
		"11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"id = $2::uuid", "expires_at > now()", "FOR UPDATE"} {
		if !strings.Contains(tx.query, want) {
			t.Fatalf("invite lock query missing %q: %s", want, tx.query)
		}
	}
	if strings.Contains(tx.query, "upper(code)") {
		t.Fatal("completion must not authorize from a replaceable raw invite code")
	}
	if len(tx.queryArgs) != 2 || tx.queryArgs[1] != invite.ID {
		t.Fatalf("invite lock args=%#v", tx.queryArgs)
	}
	if err := consumeBoundInvite(context.Background(), tx, testTenantID,
		"22222222-2222-2222-2222-222222222222", invite); err != nil {
		t.Fatal(err)
	}
	if len(tx.execs) != 2 {
		t.Fatalf("invite consumption statements=%d, want referral plus counter", len(tx.execs))
	}
	if strings.Contains(tx.execs[0], "ON CONFLICT DO NOTHING") {
		t.Fatal("referral conflicts must roll back registration instead of being hidden")
	}
	if !strings.Contains(tx.execs[1], "expires_at > now()") {
		t.Fatal("final invite counter update must use the database clock")
	}
}
