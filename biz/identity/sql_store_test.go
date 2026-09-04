package identity

import (
	"context"
	"database/sql/driver"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

const (
	identitySelectColumns = "tenant_id, provider, subject, user_id, email, name, email_verified, metadata"
	identitySelectBody    = identitySelectColumns + " FROM identity_links"
)

func newIdentityMockStore(t *testing.T) (*SQLStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewSQLStore(db)
	if err != nil {
		t.Fatalf("NewSQLStore() error = %v", err)
	}
	return store, mock
}

func assertIdentityMock(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}
}

func newIdentityLink(email string) Link {
	return Link{
		TenantID:      "tenant-a",
		Provider:      ProviderGoogle,
		Subject:       "sub-1",
		UserID:        "u1",
		Email:         email,
		EmailVerified: true,
		Metadata:      map[string]string{"org": "a"},
	}
}

func newIdentityLinkRow() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"tenant_id", "provider", "subject", "user_id", "email", "name", "email_verified", "metadata"}).
		AddRow("tenant-a", "google", "sub-1", "u1", "u1@example.com", "", true, `{"org":"a"}`)
}

func TestIdentitySQLStoreNewIgnoresNilOption(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store, err := NewSQLStore(db, nil, WithTableName("identity_links"))
	if err != nil {
		t.Fatalf("NewSQLStore(nil option) error = %v", err)
	}
	if store.table != "identity_links" {
		t.Fatalf("NewSQLStore() table = %q, want identity_links", store.table)
	}
	assertIdentityMock(t, mock)
}

func TestIdentitySQLStoreLinkInsertsNewLink(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newIdentityMockStore(t)
	link := newIdentityLink("u1@example.com")

	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT " + identitySelectBody + " WHERE tenant_id = ? AND provider = ? AND subject = ?"))
	get.WithArgs("tenant-a", "google", "sub-1").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "provider", "subject", "user_id", "email", "name", "email_verified", "metadata"}))
	ins := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO identity_links (tenant_id, provider, subject, user_id, email, name, email_verified, metadata) VALUES (?, ?, ?, ?, ?, ?, ?, ?)"))
	ins.WithArgs("tenant-a", "google", "sub-1", "u1", "u1@example.com", "", true, `{"org":"a"}`).WillReturnResult(sqlmock.NewResult(1, 1))

	if err := store.Link(ctx, link); err != nil {
		t.Fatalf("Link() error = %v", err)
	}
	assertIdentityMock(t, mock)
}

func TestIdentitySQLStoreLinkUpdatesExistingLinkToSameUser(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newIdentityMockStore(t)
	link := newIdentityLink("u1@example.com")

	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT " + identitySelectBody + " WHERE tenant_id = ? AND provider = ? AND subject = ?"))
	get.WithArgs("tenant-a", "google", "sub-1").WillReturnRows(newIdentityLinkRow())
	upd := mock.ExpectExec(regexp.QuoteMeta("UPDATE identity_links SET email = ?, name = ?, email_verified = ?, metadata = ? WHERE tenant_id = ? AND provider = ? AND subject = ? AND user_id = ?"))
	upd.WithArgs("u1@example.com", "", true, `{"org":"a"}`, "tenant-a", "google", "sub-1", "u1").WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.Link(ctx, link); err != nil {
		t.Fatalf("Link(update) error = %v", err)
	}
	assertIdentityMock(t, mock)
}

func TestIdentitySQLStoreLinkDetectsUserIDConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newIdentityMockStore(t)
	link := newIdentityLink("u1@example.com") // wants user u1

	replacement := sqlmock.NewRows([]string{"tenant_id", "provider", "subject", "user_id", "email", "name", "email_verified", "metadata"}).
		AddRow("tenant-a", "google", "sub-1", "u2", "u2@example.com", "", true, `{}`)
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT " + identitySelectBody + " WHERE tenant_id = ? AND provider = ? AND subject = ?"))
	get.WithArgs("tenant-a", "google", "sub-1").WillReturnRows(replacement)

	if err := store.Link(ctx, link); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("Link(conflicting user) error = %v, want ErrIdentityConflict", err)
	}
	assertIdentityMock(t, mock)
}

func TestIdentitySQLStoreLinkFailsAfterBothInsertAttemptsConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newIdentityMockStore(t)
	link := newIdentityLink("u1@example.com")
	dupErr := errors.New("pq: duplicate key value violates unique constraint")

	for attempt := 0; attempt < 2; attempt++ {
		get := mock.ExpectQuery(regexp.QuoteMeta("SELECT " + identitySelectBody + " WHERE tenant_id = ? AND provider = ? AND subject = ?"))
		get.WithArgs("tenant-a", "google", "sub-1").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "provider", "subject", "user_id", "email", "name", "email_verified", "metadata"}))
		ins := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO identity_links (tenant_id, provider, subject, user_id, email, name, email_verified, metadata) VALUES (?, ?, ?, ?, ?, ?, ?, ?)"))
		ins.WithArgs("tenant-a", "google", "sub-1", "u1", "u1@example.com", "", true, `{"org":"a"}`).WillReturnError(dupErr)
	}

	if err := store.Link(ctx, link); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("Link(two conflicting inserts) error = %v, want ErrIdentityConflict", err)
	}
	assertIdentityMock(t, mock)
}

func TestIdentitySQLStoreLinkPropagatesGetByExternalError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newIdentityMockStore(t)
	link := newIdentityLink("u1@example.com")
	boom := errors.New("boom")

	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT " + identitySelectBody + " WHERE tenant_id = ? AND provider = ? AND subject = ?"))
	get.WithArgs("tenant-a", "google", "sub-1").WillReturnError(boom)

	if err := store.Link(ctx, link); !errors.Is(err, boom) {
		t.Fatalf("Link(get error) error = %v, want boom", err)
	}
	assertIdentityMock(t, mock)
}

func TestIdentitySQLStoreLinkRejectsInvalidLink(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newIdentityMockStore(t)
	link := newIdentityLink("") // validate() requires non-empty email

	if err := store.Link(ctx, link); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("Link(invalid) error = %v, want ErrInvalidIdentity", err)
	}
	assertIdentityMock(t, mock)
}

func TestIdentitySQLStoreLinkHonorsCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store, mock := newIdentityMockStore(t)
	link := newIdentityLink("u1@example.com")

	if err := store.Link(ctx, link); !errors.Is(err, context.Canceled) {
		t.Fatalf("Link(cancelled) error = %v, want context.Canceled", err)
	}
	assertIdentityMock(t, mock)
}

func TestIdentitySQLStoreGetByExternal(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name    string
		rows    func() *sqlmock.Rows
		wantErr error
		want    Link
	}{
		{
			name:    "found",
			rows:    newIdentityLinkRow,
			wantErr: nil,
			want:    newIdentityLink("u1@example.com"),
		},
		{
			name:    "not found",
			rows:    func() *sqlmock.Rows { return sqlmock.NewRows([]string{"tenant_id"}) },
			wantErr: ErrIdentityNotFound,
		},
		{
			name:    "query error",
			rows:    func() *sqlmock.Rows { return sqlmock.NewRows([]string{"tenant_id"}) },
			wantErr: boom,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store, mock := newIdentityMockStore(t)
			q := mock.ExpectQuery(regexp.QuoteMeta("SELECT " + identitySelectBody + " WHERE tenant_id = ? AND provider = ? AND subject = ?"))
			if tt.wantErr != nil {
				q.WillReturnError(tt.wantErr)
			} else {
				q.WillReturnRows(tt.rows())
			}
			got, err := store.GetByExternal(ctx, "tenant-a", ProviderGoogle, "sub-1")
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("GetByExternal() error = %v, want %v", err, tt.wantErr)
				}
				assertIdentityMock(t, mock)
				return
			}
			if err != nil {
				t.Fatalf("GetByExternal() error = %v", err)
			}
			if got.TenantID != tt.want.TenantID || got.UserID != tt.want.UserID || got.Provider != tt.want.Provider || got.Email != tt.want.Email {
				t.Fatalf("GetByExternal() = %+v, want %+v", got, tt.want)
			}
			assertIdentityMock(t, mock)
		})
	}
}

func TestIdentitySQLStoreGetByExternalRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newIdentityMockStore(t)
	if _, err := store.GetByExternal(ctx, "", ProviderGoogle, "sub-1"); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("GetByExternal(empty tenant) error = %v, want ErrInvalidIdentity", err)
	}
	if _, err := store.GetByExternal(ctx, "tenant-a", ProviderGoogle, "  "); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("GetByExternal(empty subject) error = %v, want ErrInvalidIdentity", err)
	}
	assertIdentityMock(t, mock)
}

func TestIdentitySQLStoreGetByUser(t *testing.T) {
	rows := sqlmock.NewRows([]string{"tenant_id", "provider", "subject", "user_id", "email", "name", "email_verified", "metadata"}).
		AddRow("tenant-a", "google", "sub-1", "u1", "u1@example.com", "", true, `{}`).
		AddRow("tenant-a", "microsoft", "sub-2", "u1", "u1@example.com", "", false, `{}`)
	ctx := context.Background()
	store, mock := newIdentityMockStore(t)
	q := mock.ExpectQuery(regexp.QuoteMeta("SELECT " + identitySelectColumns + " FROM identity_links WHERE tenant_id = ? AND user_id = ? ORDER BY provider, subject"))
	q.WithArgs("tenant-a", "u1").WillReturnRows(rows)

	links, err := store.GetByUser(ctx, "tenant-a", "u1")
	if err != nil {
		t.Fatalf("GetByUser() error = %v", err)
	}
	if len(links) != 2 || links[0].Provider != ProviderGoogle || links[1].Provider != ProviderMicrosoft {
		t.Fatalf("GetByUser() = %+v, want 2 links ordered by provider", links)
	}
	assertIdentityMock(t, mock)
}

func TestIdentitySQLStoreGetByUserErrorBranches(t *testing.T) {
	t.Run("query error", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store, mock := newIdentityMockStore(t)
		q := mock.ExpectQuery(regexp.QuoteMeta("SELECT " + identitySelectColumns + " FROM identity_links WHERE tenant_id = ? AND user_id = ? ORDER BY provider, subject"))
		boom := errors.New("boom")
		q.WithArgs("tenant-a", "u1").WillReturnError(boom)
		if _, err := store.GetByUser(ctx, "tenant-a", "u1"); !errors.Is(err, boom) {
			t.Fatalf("GetByUser(query error) error = %v, want boom", err)
		}
		assertIdentityMock(t, mock)
	})

	t.Run("scan error", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store, mock := newIdentityMockStore(t)
		bad := sqlmock.NewRows([]string{"tenant_id", "provider", "subject", "user_id", "email", "name", "email_verified", "metadata"}).
			AddRow("tenant-a", "google", "sub-1", "u1", "u1@example.com", "", true, "not-json")
		q := mock.ExpectQuery(regexp.QuoteMeta("SELECT " + identitySelectColumns + " FROM identity_links WHERE tenant_id = ? AND user_id = ? ORDER BY provider, subject"))
		q.WithArgs("tenant-a", "u1").WillReturnRows(bad)
		if _, err := store.GetByUser(ctx, "tenant-a", "u1"); err == nil {
			t.Fatal("GetByUser(bad metadata) error = nil, want unmarshal error")
		}
		assertIdentityMock(t, mock)
	})

	t.Run("invalid input", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store, mock := newIdentityMockStore(t)
		if _, err := store.GetByUser(ctx, "", "u1"); !errors.Is(err, ErrInvalidIdentity) {
			t.Fatalf("GetByUser(empty tenant) error = %v, want ErrInvalidIdentity", err)
		}
		if _, err := store.GetByUser(ctx, "tenant-a", "  "); !errors.Is(err, ErrInvalidIdentity) {
			t.Fatalf("GetByUser(empty user) error = %v, want ErrInvalidIdentity", err)
		}
		assertIdentityMock(t, mock)
	})
}

func TestIdentitySQLStoreUnlink(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name    string
		rows    int64
		wantErr error
	}{
		{name: "removed", rows: 1, wantErr: nil},
		{name: "not found", rows: 0, wantErr: ErrIdentityNotFound},
		{name: "exec error", wantErr: boom},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store, mock := newIdentityMockStore(t)
			e := mock.ExpectExec(regexp.QuoteMeta("DELETE FROM identity_links WHERE tenant_id = ? AND provider = ? AND subject = ?"))
			if tt.wantErr != nil {
				e.WillReturnError(tt.wantErr)
			} else {
				e.WithArgs("tenant-a", "google", "sub-1").WillReturnResult(sqlmock.NewResult(0, tt.rows))
			}
			err := store.Unlink(ctx, "tenant-a", ProviderGoogle, "sub-1")
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Unlink() error = %v, want %v", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("Unlink() error = %v", err)
			}
			assertIdentityMock(t, mock)
		})
	}
}

func TestIdentitySQLStoreUnlinkRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newIdentityMockStore(t)
	if err := store.Unlink(ctx, "", ProviderGoogle, "sub-1"); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("Unlink(empty tenant) error = %v, want ErrInvalidIdentity", err)
	}
	if err := store.Unlink(ctx, "tenant-a", "", "sub-1"); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("Unlink(empty provider) error = %v, want ErrInvalidIdentity", err)
	}
	assertIdentityMock(t, mock)
}

func TestIdentitySQLStoreNoopUpdateReconfirmsExistingLink(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newIdentityMockStore(t)
	link := newIdentityLink("u1@example.com")

	upd := mock.ExpectExec(regexp.QuoteMeta("UPDATE identity_links SET email = ?, name = ?, email_verified = ?, metadata = ? WHERE tenant_id = ? AND provider = ? AND subject = ? AND user_id = ?"))
	upd.WithArgs("u1@example.com", "", true, `{"org":"a"}`, "tenant-a", "google", "sub-1", "u1").WillReturnResult(sqlmock.NewResult(0, 0))
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT " + identitySelectBody + " WHERE tenant_id = ? AND provider = ? AND subject = ?"))
	get.WithArgs("tenant-a", "google", "sub-1").WillReturnRows(newIdentityLinkRow())

	if err := store.updateLink(ctx, link); err != nil {
		t.Fatalf("updateLink() error = %v", err)
	}
	assertIdentityMock(t, mock)
}

func TestIdentitySQLStoreNoopUpdateReportsReplacementConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newIdentityMockStore(t)
	link := newIdentityLink("u1@example.com")

	upd := mock.ExpectExec(regexp.QuoteMeta("UPDATE identity_links SET email = ?, name = ?, email_verified = ?, metadata = ? WHERE tenant_id = ? AND provider = ? AND subject = ? AND user_id = ?"))
	upd.WithArgs("u1@example.com", "", true, `{"org":"a"}`, "tenant-a", "google", "sub-1", "u1").WillReturnResult(sqlmock.NewResult(0, 0))
	replacement := sqlmock.NewRows([]string{"tenant_id", "provider", "subject", "user_id", "email", "name", "email_verified", "metadata"}).
		AddRow("tenant-a", "google", "sub-1", "u1", "replacement@example.com", "", true, `{}`)
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT " + identitySelectBody + " WHERE tenant_id = ? AND provider = ? AND subject = ?"))
	get.WithArgs("tenant-a", "google", "sub-1").WillReturnRows(replacement)

	if err := store.updateLink(ctx, link); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("updateLink(noop after replacement) error = %v, want ErrIdentityConflict", err)
	}
	assertIdentityMock(t, mock)
}

func TestIdentitySQLStoreUpdateLinkExecError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newIdentityMockStore(t)
	link := newIdentityLink("u1@example.com")
	boom := errors.New("boom")
	upd := mock.ExpectExec(regexp.QuoteMeta("UPDATE identity_links SET email = ?, name = ?, email_verified = ?, metadata = ? WHERE tenant_id = ? AND provider = ? AND subject = ? AND user_id = ?"))
	upd.WithArgs("u1@example.com", "", true, `{"org":"a"}`, "tenant-a", "google", "sub-1", "u1").WillReturnError(boom)
	if err := store.updateLink(ctx, link); !errors.Is(err, boom) {
		t.Fatalf("updateLink(exec error) error = %v, want boom", err)
	}
	assertIdentityMock(t, mock)
}

type identityResultStub struct {
	affected int64
	err      error
}

func (r identityResultStub) RowsAffected() (int64, error) { return r.affected, r.err }
func (r identityResultStub) LastInsertId() (int64, error) { return 0, nil }

func TestIdentitySQLStoreRequireAffectedLinkBranches(t *testing.T) {
	t.Parallel()
	if err := requireAffectedLink(identityResultStub{}); !errors.Is(err, ErrIdentityNotFound) {
		t.Fatalf("requireAffectedLink(0) error = %v, want ErrIdentityNotFound", err)
	}
	boom := errors.New("boom")
	if err := requireAffectedLink(identityResultStub{err: boom}); !errors.Is(err, boom) {
		t.Fatalf("requireAffectedLink(err) error = %v, want boom", err)
	}
	if err := requireAffectedLink(driver.RowsAffected(1)); err != nil {
		t.Fatalf("requireAffectedLink(1) error = %v, want nil", err)
	}
}

func TestIdentitySQLStoreScanLinkErrorBranches(t *testing.T) {
	t.Parallel()
	scanErr := errors.New("scan failed")
	if _, err := scanLink(linkScannerFunc(func(...any) error { return scanErr })); !errors.Is(err, scanErr) {
		t.Fatalf("scanLink(scan error) error = %v, want scan failed", err)
	}
	if _, err := scanLink(linkScannerFunc(func(dest ...any) error {
		*(dest[0].(*string)) = "tenant-a"
		*(dest[1].(*string)) = "google"
		*(dest[2].(*string)) = "sub-1"
		*(dest[3].(*string)) = "u1"
		*(dest[4].(*string)) = "u1@example.com"
		*(dest[5].(*string)) = "User"
		*(dest[6].(*bool)) = true
		*(dest[7].(*string)) = "not-json"
		return nil
	})); err == nil {
		t.Fatal("scanLink(bad metadata) error = nil, want unmarshal error")
	}
	if _, err := scanLink(linkScannerFunc(func(dest ...any) error {
		*(dest[0].(*string)) = "tenant-a"
		*(dest[1].(*string)) = "google"
		*(dest[2].(*string)) = "sub-1"
		*(dest[3].(*string)) = "u1"
		*(dest[4].(*string)) = "" // missing email -> validate fails
		*(dest[5].(*string)) = "User"
		*(dest[6].(*bool)) = true
		*(dest[7].(*string)) = `{}`
		return nil
	})); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("scanLink(invalid link) error = %v, want ErrInvalidIdentity", err)
	}
}