//go:build linux && (amd64 || arm64)

package ca42runner

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42artifactsv2"
	"github.com/aegispanel/aegis/internal/platform/releasejournal"
)

type v3SessionProbe struct {
	mu              sync.Mutex
	order           []string
	values          ca42artifactsv2.JournalBindingValues
	inventory       *v3InventoryProbe
	control         *v3ControlProbe
	authority       *v3AuthorityProbe
	journal         *v3JournalProbe
	openJournalErr  error
	typedNilJournal bool
	invCloseErr     error
	ctlCloseErr     error
	authCloseErr    error
	journalCloseErr error
}

type v3InventoryProbe struct {
	probe      *v3SessionProbe
	revalidate func(context.Context) error
	onClose    func()
	closed     bool
}

func (p *v3InventoryProbe) Revalidate(ctx context.Context) error {
	p.probe.record("inventory.revalidate")
	if p.revalidate != nil {
		return p.revalidate(ctx)
	}
	return ctx.Err()
}

func (p *v3InventoryProbe) Close() error {
	p.probe.record("inventory.close")
	p.closed = true
	if p.onClose != nil {
		p.onClose()
	}
	return p.probe.invCloseErr
}

type v3JournalProbe struct {
	probe   *v3SessionProbe
	inspect func(context.Context) error
	onClose func()
	closed  bool
}

type v3ControlProbe struct {
	probe      *v3SessionProbe
	revalidate func(context.Context) error
	onClose    func()
	closed     bool
}

type v3AuthorityProbe struct {
	probe      *v3SessionProbe
	revalidate func(context.Context) error
	onClose    func()
	closed     bool
}

func (p *v3AuthorityProbe) Revalidate(ctx context.Context) error {
	p.probe.record("authority.revalidate")
	if p.revalidate != nil {
		return p.revalidate(ctx)
	}
	return ctx.Err()
}

func (p *v3AuthorityProbe) Close() error {
	p.probe.record("authority.close")
	p.closed = true
	if p.onClose != nil {
		p.onClose()
	}
	return p.probe.authCloseErr
}

func (p *v3ControlProbe) Revalidate(ctx context.Context) error {
	p.probe.record("control.revalidate")
	if p.revalidate != nil {
		return p.revalidate(ctx)
	}
	return ctx.Err()
}

func (p *v3ControlProbe) Close() error {
	p.probe.record("control.close")
	p.closed = true
	if p.onClose != nil {
		p.onClose()
	}
	return p.probe.ctlCloseErr
}

func (p *v3JournalProbe) InspectLayoutSwitchedFor(ca42artifactsv2.JournalBinding, time.Time) (releasejournal.V3Snapshot, error) {
	p.probe.record("journal.inspect")
	return releasejournal.V3Snapshot{}, nil
}

func (p *v3JournalProbe) Close() error {
	p.probe.record("journal.close")
	p.closed = true
	if p.onClose != nil {
		p.onClose()
	}
	return p.probe.journalCloseErr
}

func (p *v3SessionProbe) record(value string) {
	p.mu.Lock()
	p.order = append(p.order, value)
	p.mu.Unlock()
}

func (p *v3SessionProbe) ops(now func() time.Time) v3VerificationOps {
	return v3VerificationOps{
		now: now,
		verifySet: func(set ca42artifactsv2.Set, _ time.Time) (ca42artifactsv2.Set, error) {
			p.record("set.verify")
			return set, nil
		},
		journalBinding: func(_ ca42artifactsv2.Set, _ time.Time) (ca42artifactsv2.JournalBinding, error) {
			p.record("binding.mint")
			return ca42artifactsv2.JournalBinding{}, nil
		},
		bindingValues: func(ca42artifactsv2.JournalBinding, time.Time) (ca42artifactsv2.JournalBindingValues, error) {
			p.record("binding.verify")
			return p.values, nil
		},
		openJournal: func(string) (v3JournalLease, error) {
			p.record("journal.open")
			if p.openJournalErr != nil {
				return nil, p.openJournalErr
			}
			if p.typedNilJournal {
				var journal *v3JournalProbe
				return journal, nil
			}
			p.journal = &v3JournalProbe{probe: p}
			return p.journal, nil
		},
	}
}

func newV3SessionProbe() *v3SessionProbe {
	return &v3SessionProbe{values: ca42artifactsv2.JournalBindingValues{AttemptID: "attempt-ca42-1",
		ReleaseContractCoreSHA256: "core", ReleaseJournalHeadSHA256: "head",
		ReleaseJournalSnapshotSHA256: "manifest", ArtifactSetBindingSHA256: "set"}}
}

func TestV3VerificationSessionAcquiresVerifiesAndSharesClose(t *testing.T) {
	probe := newV3SessionProbe()
	now := time.Unix(1_700_000_100, 0).UTC()
	session, err := openV3SessionProbeWithOps(context.Background(), probe, probe.ops(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	copySession := *session
	if probe.authority == nil || probe.control == nil || probe.inventory == nil || probe.journal == nil {
		t.Fatal("complete session was not acquired")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := copySession.Close(); err != nil {
		t.Fatal(err)
	}
	if !probe.authority.closed || !probe.control.closed || !probe.inventory.closed || !probe.journal.closed {
		t.Fatal("shared close did not close all owned capabilities")
	}
	got := probe.order[len(probe.order)-4:]
	if got[0] != "journal.close" || got[1] != "inventory.close" || got[2] != "control.close" || got[3] != "authority.close" {
		t.Fatalf("reverse close order invalid: %v", got)
	}
}

func TestV3VerificationSessionAdoptsHandoffWithoutReopeningInventory(t *testing.T) {
	probe := newV3SessionProbe()
	probe.inventory = &v3InventoryProbe{probe: probe}
	probe.control = &v3ControlProbe{probe: probe}
	probe.authority = &v3AuthorityProbe{probe: probe}
	handoff := &v3ArtifactHandoff{state: &v3ArtifactHandoffState{
		set: ca42artifactsv2.Set{}, authority: probe.authority, inventory: probe.inventory, control: probe.control,
	}}
	now := time.Unix(1_700_000_100, 0).UTC()
	session, err := openV3VerificationSessionFromHandoffWithOps(context.Background(), handoff, probe.ops(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range probe.order {
		if event == "inventory.open" {
			t.Fatal("adopted session reopened production inventory")
		}
	}
	if err := handoff.Close(); err != nil {
		t.Fatal(err)
	}
	if probe.inventory.closed || probe.control.closed || probe.authority.closed {
		t.Fatal("consumed handoff closed session-owned resources")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if !probe.inventory.closed || !probe.control.closed || !probe.authority.closed || probe.journal == nil || !probe.journal.closed {
		t.Fatal("adopted session did not close all resources")
	}
	got := probe.order[len(probe.order)-4:]
	if got[0] != "journal.close" || got[1] != "inventory.close" || got[2] != "control.close" || got[3] != "authority.close" {
		t.Fatalf("adopted close order invalid: %v", got)
	}
	if _, err := handoff.take(); !errors.Is(err, errV3ArtifactHandoffUnavailable) {
		t.Fatalf("consumed handoff revived: %v", err)
	}
}

func TestV3VerificationSessionAdoptedFirstFailureClosesInventoryThenControl(t *testing.T) {
	probe := newV3SessionProbe()
	probe.inventory = &v3InventoryProbe{probe: probe}
	probe.control = &v3ControlProbe{probe: probe}
	probe.authority = &v3AuthorityProbe{probe: probe}
	handoff := &v3ArtifactHandoff{state: &v3ArtifactHandoffState{
		set: ca42artifactsv2.Set{}, authority: probe.authority, inventory: probe.inventory, control: probe.control,
	}}
	now := time.Unix(1_700_000_100, 0).UTC()
	ops := probe.ops(func() time.Time { return now })
	sentinel := errors.New("first adopted verification failed")
	ops.verifySet = func(ca42artifactsv2.Set, time.Time) (ca42artifactsv2.Set, error) {
		probe.record("set.verify")
		return ca42artifactsv2.Set{}, sentinel
	}
	session, err := openV3VerificationSessionFromHandoffWithOps(context.Background(), handoff, ops)
	if session != nil || !errors.Is(err, sentinel) {
		t.Fatalf("first adopted failure was not preserved: session=%v err=%v", session, err)
	}
	if !probe.inventory.closed || !probe.control.closed || !probe.authority.closed {
		t.Fatal("first adopted failure leaked transferred resources")
	}
	got := probe.order[len(probe.order)-3:]
	if got[0] != "inventory.close" || got[1] != "control.close" || got[2] != "authority.close" {
		t.Fatalf("first adopted rollback order invalid: %v", got)
	}
}

func TestV3VerificationSessionHandoffRequiresAuthorityAndControl(t *testing.T) {
	probe := newV3SessionProbe()
	probe.inventory = &v3InventoryProbe{probe: probe}
	probe.control = &v3ControlProbe{probe: probe}
	for name, handoff := range map[string]*v3ArtifactHandoff{
		"missing authority": {state: &v3ArtifactHandoffState{set: ca42artifactsv2.Set{}, inventory: probe.inventory, control: probe.control}},
		"missing control":   {state: &v3ArtifactHandoffState{set: ca42artifactsv2.Set{}, authority: &v3AuthorityProbe{probe: probe}, inventory: probe.inventory}},
	} {
		t.Run(name, func(t *testing.T) {
			session, err := openV3VerificationSessionFromHandoffWithOps(context.Background(), handoff, probe.ops(func() time.Time {
				return time.Unix(1_700_000_100, 0).UTC()
			}))
			if session != nil || !errors.Is(err, errV3ArtifactHandoffUnavailable) {
				t.Fatalf("incomplete handoff published: session=%v err=%v", session, err)
			}
		})
	}
}

func TestV3ProductionSessionAPIRejectsCallerSuppliedArtifactSet(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(current), "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	type parsedSource struct {
		file    *ast.File
		aliases map[string]bool
	}
	type wrapperDefinition struct {
		expr    ast.Expr
		aliases map[string]bool
	}
	var sources []parsedSource
	wrappers := make(map[string]wrapperDefinition)
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		aliases := artifactSetImportAliases(file)
		sources = append(sources, parsedSource{file: file, aliases: aliases})
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.TYPE {
				continue
			}
			for _, spec := range general.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if ok {
					wrappers[typeSpec.Name.Name] = wrapperDefinition{expr: typeSpec.Type, aliases: aliases}
				}
			}
		}
	}
	tainted := make(map[string]bool)
	for changed := true; changed; {
		changed = false
		for name, definition := range wrappers {
			if !tainted[name] && v3TypeContainsArtifactSet(definition.expr, definition.aliases, tainted) {
				tainted[name], changed = true, true
			}
		}
	}
	openers := 0
	for _, source := range sources {
		for _, declaration := range source.file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || !function.Name.IsExported() {
				continue
			}
			if function.Type.Results != nil {
				for _, field := range function.Type.Results.List {
					pointer, ok := field.Type.(*ast.StarExpr)
					if !ok {
						continue
					}
					identifier, identifierOK := pointer.X.(*ast.Ident)
					if identifierOK && identifier.Name == "V3VerificationSession" {
						if function.Name.Name != "OpenProductionV3VerificationSession" {
							t.Fatalf("unexpected exported V3 session opener %s", function.Name.Name)
						}
						openers++
					}
				}
			}
			if function.Type.Params == nil {
				continue
			}
			for _, field := range function.Type.Params.List {
				if v3TypeContainsArtifactSet(field.Type, source.aliases, tainted) {
					t.Fatalf("exported function %s accepts caller-supplied ArtifactSet directly or through a wrapper", function.Name.Name)
				}
			}
		}
	}
	if openers != 2 { // one supported build and one fail-closed unsupported build
		t.Fatalf("V3 production session opener inventory=%d want=2", openers)
	}
}

func artifactSetImportAliases(file *ast.File) map[string]bool {
	aliases := make(map[string]bool)
	for _, imported := range file.Imports {
		if imported.Path == nil || strings.Trim(imported.Path.Value, `"`) != "github.com/aegispanel/aegis/internal/platform/clientauth/ca42artifactsv2" {
			continue
		}
		name := "ca42artifactsv2"
		if imported.Name != nil {
			name = imported.Name.Name
		}
		if name != "_" && name != "." {
			aliases[name] = true
		}
	}
	return aliases
}

func v3TypeContainsArtifactSet(expr ast.Expr, aliases, tainted map[string]bool) bool {
	switch candidate := expr.(type) {
	case *ast.Ident:
		return tainted[candidate.Name]
	case *ast.SelectorExpr:
		owner, ok := candidate.X.(*ast.Ident)
		return ok && aliases[owner.Name] && candidate.Sel.Name == "Set"
	case *ast.StarExpr:
		return v3TypeContainsArtifactSet(candidate.X, aliases, tainted)
	case *ast.ArrayType:
		return v3TypeContainsArtifactSet(candidate.Elt, aliases, tainted)
	case *ast.Ellipsis:
		return v3TypeContainsArtifactSet(candidate.Elt, aliases, tainted)
	case *ast.ChanType:
		return v3TypeContainsArtifactSet(candidate.Value, aliases, tainted)
	case *ast.MapType:
		return v3TypeContainsArtifactSet(candidate.Key, aliases, tainted) || v3TypeContainsArtifactSet(candidate.Value, aliases, tainted)
	case *ast.ParenExpr:
		return v3TypeContainsArtifactSet(candidate.X, aliases, tainted)
	case *ast.IndexExpr:
		return v3TypeContainsArtifactSet(candidate.X, aliases, tainted) || v3TypeContainsArtifactSet(candidate.Index, aliases, tainted)
	case *ast.IndexListExpr:
		if v3TypeContainsArtifactSet(candidate.X, aliases, tainted) {
			return true
		}
		for _, index := range candidate.Indices {
			if v3TypeContainsArtifactSet(index, aliases, tainted) {
				return true
			}
		}
	case *ast.StructType:
		return v3FieldListContainsArtifactSet(candidate.Fields, aliases, tainted)
	case *ast.InterfaceType:
		return v3FieldListContainsArtifactSet(candidate.Methods, aliases, tainted)
	case *ast.FuncType:
		return v3FieldListContainsArtifactSet(candidate.Params, aliases, tainted) ||
			v3FieldListContainsArtifactSet(candidate.Results, aliases, tainted)
	}
	return false
}

func v3FieldListContainsArtifactSet(fields *ast.FieldList, aliases, tainted map[string]bool) bool {
	if fields == nil {
		return false
	}
	for _, field := range fields.List {
		if v3TypeContainsArtifactSet(field.Type, aliases, tainted) {
			return true
		}
	}
	return false
}

func TestV3ArtifactSetSurfaceAnalysisFollowsAliasesAndWrappers(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", `package fixture
import artifacts "github.com/aegispanel/aegis/internal/platform/clientauth/ca42artifactsv2"
type Direct = artifacts.Set
type Pointer *Direct
type Wrapped struct { Values []Pointer }
type Callback func(map[string]Wrapped) error
type Clean struct { Value string }
`, 0)
	if err != nil {
		t.Fatal(err)
	}
	aliases := artifactSetImportAliases(file)
	definitions := make(map[string]ast.Expr)
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.TYPE {
			continue
		}
		for _, spec := range general.Specs {
			typeSpec := spec.(*ast.TypeSpec)
			definitions[typeSpec.Name.Name] = typeSpec.Type
		}
	}
	tainted := make(map[string]bool)
	for changed := true; changed; {
		changed = false
		for name, expression := range definitions {
			if !tainted[name] && v3TypeContainsArtifactSet(expression, aliases, tainted) {
				tainted[name], changed = true, true
			}
		}
	}
	for _, name := range []string{"Direct", "Pointer", "Wrapped", "Callback"} {
		if !tainted[name] {
			t.Fatalf("transitive ArtifactSet wrapper %s was not detected", name)
		}
	}
	if tainted["Clean"] {
		t.Fatal("clean wrapper was classified as ArtifactSet")
	}
}

func TestV3VerificationSessionJournalBusyRollsBackInventory(t *testing.T) {
	probe := newV3SessionProbe()
	probe.openJournalErr = releasejournal.ErrV3SessionBusy
	now := time.Unix(1_700_000_100, 0).UTC()
	session, err := openV3SessionProbeWithOps(context.Background(), probe, probe.ops(func() time.Time { return now }))
	if session != nil || !errors.Is(err, releasejournal.ErrV3SessionBusy) {
		t.Fatalf("journal busy was not preserved: session=%v err=%v", session, err)
	}
	if !probe.inventory.closed || !probe.control.closed || !probe.authority.closed {
		t.Fatal("journal busy did not roll back four-capability ownership")
	}
}

func TestV3VerificationSessionTypedNilJournalRollsBackInventory(t *testing.T) {
	probe := newV3SessionProbe()
	probe.typedNilJournal = true
	now := time.Unix(1_700_000_100, 0).UTC()
	session, err := openV3SessionProbeWithOps(context.Background(), probe, probe.ops(func() time.Time { return now }))
	if session != nil || err == nil {
		t.Fatalf("typed nil journal published: session=%v err=%v", session, err)
	}
	if !probe.inventory.closed || !probe.control.closed || !probe.authority.closed {
		t.Fatal("typed nil journal did not roll back four-capability ownership")
	}
}

func TestV3VerificationSessionClockRegressionRollsBack(t *testing.T) {
	probe := newV3SessionProbe()
	base := time.Unix(1_700_000_100, 0).UTC()
	calls := 0
	session, err := openV3SessionProbeWithOps(context.Background(), probe, probe.ops(func() time.Time {
		calls++
		if calls < 3 {
			return base
		}
		return base.Add(-time.Second)
	}))
	if session != nil || err == nil {
		t.Fatalf("clock regression published session: session=%v err=%v", session, err)
	}
	if !probe.inventory.closed || !probe.control.closed || !probe.authority.closed || probe.journal == nil || !probe.journal.closed {
		t.Fatal("clock regression did not close partial session")
	}
}

func TestV3VerificationSessionCloseCancelsActiveVerify(t *testing.T) {
	probe := newV3SessionProbe()
	base := time.Unix(1_700_000_100, 0).UTC()
	session, err := openV3SessionProbeWithOps(context.Background(), probe, probe.ops(func() time.Time { return base }))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	probe.inventory.revalidate = func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	verifyDone := make(chan error, 1)
	go func() { verifyDone <- session.Verify(context.Background()) }()
	<-started
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-verifyDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("active verify was not canceled: %v", err)
	}
	if !probe.inventory.closed || !probe.control.closed || !probe.authority.closed || !probe.journal.closed {
		t.Fatal("canceled verification left capabilities open")
	}
}

func TestV3VerificationSessionVerifyPanicPoisonsAndReverseClosesAll(t *testing.T) {
	probe, session := openAdoptedV3SessionProbe(t)
	probe.authority.revalidate = func(context.Context) error { panic("v3-authority-panic") }
	func() {
		defer func() {
			if recovered := recover(); recovered != "v3-authority-panic" {
				t.Fatalf("unexpected panic: %v", recovered)
			}
		}()
		_ = session.Verify(context.Background())
	}()
	assertV3SessionClosedInReverse(t, probe)
	if err := session.Verify(context.Background()); err == nil {
		t.Fatal("panic-poisoned session remained usable")
	}
}

func TestV3VerificationSessionVerifyGoexitPoisonsAndReverseClosesAll(t *testing.T) {
	probe, session := openAdoptedV3SessionProbe(t)
	probe.authority.revalidate = func(context.Context) error {
		runtime.Goexit()
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = session.Verify(context.Background())
		t.Error("runtime.Goexit returned")
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runtime.Goexit cleanup deadlocked")
	}
	assertV3SessionClosedInReverse(t, probe)
}

func TestV3VerificationSessionCloseRetainsJoinedErrorsAcrossCopies(t *testing.T) {
	probe, session := openAdoptedV3SessionProbe(t)
	probe.journalCloseErr = errors.New("journal-close")
	probe.invCloseErr = errors.New("inventory-close")
	probe.ctlCloseErr = errors.New("control-close")
	probe.authCloseErr = errors.New("authority-close")
	copySession := *session
	first := session.Close()
	second := copySession.Close()
	for _, sentinel := range []error{probe.journalCloseErr, probe.invCloseErr, probe.ctlCloseErr, probe.authCloseErr} {
		if !errors.Is(first, sentinel) || !errors.Is(second, sentinel) {
			t.Fatalf("close result did not retain %v: first=%v second=%v", sentinel, first, second)
		}
	}
	assertV3SessionClosedInReverse(t, probe)
}

func TestV3VerificationSessionPreCanceledVerifyRetainsCloseErrors(t *testing.T) {
	probe, session := openAdoptedV3SessionProbe(t)
	probe.journalCloseErr = errors.New("journal-close")
	probe.invCloseErr = errors.New("inventory-close")
	probe.ctlCloseErr = errors.New("control-close")
	probe.authCloseErr = errors.New("authority-close")
	copySession := *session
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	verifyErr := session.Verify(ctx)
	if !errors.Is(verifyErr, context.Canceled) {
		t.Fatalf("pre-canceled verification lost context error: %v", verifyErr)
	}
	closeErr := copySession.Close()
	for _, sentinel := range []error{probe.journalCloseErr, probe.invCloseErr, probe.ctlCloseErr, probe.authCloseErr} {
		if !errors.Is(verifyErr, sentinel) || !errors.Is(closeErr, sentinel) {
			t.Fatalf("pre-canceled result did not retain %v: verify=%v close=%v", sentinel, verifyErr, closeErr)
		}
	}
}

func TestV3VerificationSessionChildClosePanicAndGoexitReleaseRemainingOwners(t *testing.T) {
	t.Run("journal-panic", func(t *testing.T) {
		probe, session := openAdoptedV3SessionProbe(t)
		probe.journal.onClose = func() { panic("journal-close-panic") }
		func() {
			defer func() {
				if recovered := recover(); recovered != "journal-close-panic" {
					t.Fatalf("unexpected panic: %v", recovered)
				}
			}()
			_ = session.Close()
		}()
		assertV3SessionClosedInReverse(t, probe)
	})
	t.Run("inventory-goexit", func(t *testing.T) {
		probe, session := openAdoptedV3SessionProbe(t)
		probe.inventory.onClose = runtime.Goexit
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = session.Close()
			t.Error("runtime.Goexit returned")
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("child Close Goexit cleanup deadlocked")
		}
		assertV3SessionClosedInReverse(t, probe)
	})
}

func openAdoptedV3SessionProbe(t *testing.T) (*v3SessionProbe, *V3VerificationSession) {
	t.Helper()
	probe := newV3SessionProbe()
	probe.inventory = &v3InventoryProbe{probe: probe}
	probe.control = &v3ControlProbe{probe: probe}
	probe.authority = &v3AuthorityProbe{probe: probe}
	handoff := &v3ArtifactHandoff{state: &v3ArtifactHandoffState{
		set: ca42artifactsv2.Set{}, authority: probe.authority, inventory: probe.inventory, control: probe.control,
	}}
	session, err := openV3VerificationSessionFromHandoffWithOps(context.Background(), handoff, probe.ops(func() time.Time {
		return time.Unix(1_700_000_100, 0).UTC()
	}))
	if err != nil {
		t.Fatal(err)
	}
	return probe, session
}

func openV3SessionProbeWithOps(ctx context.Context, probe *v3SessionProbe, ops v3VerificationOps) (*V3VerificationSession, error) {
	if probe.inventory == nil {
		probe.inventory = &v3InventoryProbe{probe: probe}
	}
	if probe.control == nil {
		probe.control = &v3ControlProbe{probe: probe}
	}
	if probe.authority == nil {
		probe.authority = &v3AuthorityProbe{probe: probe}
	}
	handoff := &v3ArtifactHandoff{state: &v3ArtifactHandoffState{
		set: ca42artifactsv2.Set{}, authority: probe.authority, inventory: probe.inventory, control: probe.control,
	}}
	return openV3VerificationSessionFromHandoffWithOps(ctx, handoff, ops)
}

func assertV3SessionClosedInReverse(t *testing.T, probe *v3SessionProbe) {
	t.Helper()
	if probe.journal == nil || !probe.journal.closed || !probe.inventory.closed || !probe.control.closed || !probe.authority.closed {
		t.Fatal("session did not close all four owned capabilities")
	}
	got := probe.order[len(probe.order)-4:]
	want := []string{"journal.close", "inventory.close", "control.close", "authority.close"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("reverse close order invalid: got=%v want=%v", got, want)
		}
	}
}
