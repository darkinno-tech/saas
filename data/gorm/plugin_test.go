package gormtenant

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/darkinno-tech/saas"
	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/types"
	"github.com/darkinno-tech/saas/data"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type tenantOrder struct {
	ID        uint
	TenantID  string `gorm:"column:tenant_id"`
	Name      string
	DeletedAt gorm.DeletedAt
}

func (tenantOrder) TableName() string {
	return "tenant_orders"
}

func TestPluginName(t *testing.T) {
	if got := New(Config{}).Name(); got != pluginName {
		t.Fatalf("Name() = %q, want %q", got, pluginName)
	}
}

func TestQueryAddsTenantCondition(t *testing.T) {
	db := newDryRunDB(t)
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	var orders []tenantOrder
	tx := db.WithContext(ctx).Find(&orders)
	if tx.Error != nil {
		t.Fatalf("Find() error = %v", tx.Error)
	}

	assertSQLContains(t, tx.Statement.SQL.String(), "tenant_id = ?")
	assertVarsContain(t, tx.Statement.Vars, "tenant-a")
}

func TestQueryAddsSoftDeleteConditionWhenConfigured(t *testing.T) {
	db := newDryRunDBWithConfig(t, Config{SoftDeleteField: "deleted_at"})
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	var orders []tenantOrder
	tx := db.WithContext(ctx).Find(&orders)
	if tx.Error != nil {
		t.Fatalf("Find() error = %v", tx.Error)
	}

	assertSQLContains(t, tx.Statement.SQL.String(), "tenant_id = ?")
	assertSQLContains(t, tx.Statement.SQL.String(), "deleted_at IS NULL")
}

func TestHostQueryDoesNotAddTenantCondition(t *testing.T) {
	db := newDryRunDB(t)

	var orders []tenantOrder
	tx := db.WithContext(tenantctx.WithHost(context.Background())).Find(&orders)
	if tx.Error != nil {
		t.Fatalf("Find(host) error = %v", tx.Error)
	}
	if strings.Contains(tx.Statement.SQL.String(), "tenant_id = ?") {
		t.Fatalf("host SQL contains tenant condition: %s", tx.Statement.SQL.String())
	}
}

func TestQueryRequiresTenantOrHostContext(t *testing.T) {
	db := newDryRunDB(t)

	var orders []tenantOrder
	tx := db.Find(&orders)
	if !errors.Is(tx.Error, data.ErrNoTenant) {
		t.Fatalf("Find(no context) error = %v, want ErrNoTenant", tx.Error)
	}
}

func TestCreateFillsTenantID(t *testing.T) {
	db := newDryRunDB(t)
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	order := tenantOrder{Name: "first"}
	tx := db.WithContext(ctx).Create(&order)
	if tx.Error != nil {
		t.Fatalf("Create() error = %v", tx.Error)
	}
	if order.TenantID != "tenant-a" {
		t.Fatalf("TenantID after Create() = %q, want tenant-a", order.TenantID)
	}
	assertSQLContains(t, tx.Statement.SQL.String(), "tenant_id")
	assertVarsContain(t, tx.Statement.Vars, "tenant-a")
}

func TestCreateRejectsTenantMismatch(t *testing.T) {
	db := newDryRunDB(t)
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	order := tenantOrder{TenantID: "tenant-b", Name: "first"}
	tx := db.WithContext(ctx).Create(&order)
	if !errors.Is(tx.Error, ErrTenantMismatch) {
		t.Fatalf("Create(mismatch) error = %v, want ErrTenantMismatch", tx.Error)
	}
}

func TestBulkCreateFillsTenantID(t *testing.T) {
	db := newDryRunDB(t)
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	orders := []tenantOrder{{Name: "first"}, {Name: "second"}}
	tx := BulkCreate(ctx, db, &orders)
	if tx.Error != nil {
		t.Fatalf("Create(slice) error = %v", tx.Error)
	}
	for i, order := range orders {
		if order.TenantID != "tenant-a" {
			t.Fatalf("orders[%d].TenantID = %q, want tenant-a", i, order.TenantID)
		}
	}
}

func TestUnscopedReportsErrorInTenantContext(t *testing.T) {
	db := newDryRunDB(t)
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	var orders []tenantOrder
	tx := db.WithContext(ctx).Unscoped().Find(&orders)
	if !errors.Is(tx.Error, ErrUnscopedRequiresHost) {
		t.Fatalf("Unscoped Find() error = %v, want ErrUnscopedRequiresHost", tx.Error)
	}
}

func TestUpdateAndDeleteAddTenantCondition(t *testing.T) {
	db := newDryRunDB(t)
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	update := db.WithContext(ctx).Model(&tenantOrder{}).Where("id = ?", 1).Update("name", "updated")
	if update.Error != nil {
		t.Fatalf("Update() error = %v", update.Error)
	}
	assertSQLContains(t, update.Statement.SQL.String(), "tenant_id = ?")
	assertVarsContain(t, update.Statement.Vars, "tenant-a")

	deleteTx := db.WithContext(ctx).Where("id = ?", 1).Delete(&tenantOrder{})
	if deleteTx.Error != nil {
		t.Fatalf("Delete() error = %v", deleteTx.Error)
	}
	assertSQLContains(t, deleteTx.Statement.SQL.String(), "tenant_id = ?")
	assertVarsContain(t, deleteTx.Statement.Vars, "tenant-a")
}

func TestUpdateRejectsTenantFieldChange(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	for _, test := range []struct {
		name   string
		config Config
		field  string
	}{
		{name: "default field", field: "tenant_id"},
		{name: "qualified configured field", config: Config{TenantField: "tenant_orders.tenant_id"}, field: "tenant_id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := newDryRunDBWithConfig(t, test.config)
			tx := db.WithContext(ctx).Model(&tenantOrder{}).Where("id = ?", 1).Update(test.field, "tenant-b")
			if !errors.Is(tx.Error, ErrTenantFieldUpdate) {
				t.Fatalf("Update(tenant field) error = %v, want ErrTenantFieldUpdate", tx.Error)
			}
		})
	}

	db := newDryRunDB(t)
	structUpdate := db.WithContext(ctx).Model(&tenantOrder{}).Where("id = ?", 1).Updates(tenantOrder{TenantID: "tenant-b", Name: "updated"})
	if !errors.Is(structUpdate.Error, ErrTenantFieldUpdate) {
		t.Fatalf("Updates(struct tenant field) error = %v, want ErrTenantFieldUpdate", structUpdate.Error)
	}
	expressionUpdate := db.WithContext(ctx).Model(&tenantOrder{}).Where("id = ?", 1).Update("tenant_id", gorm.Expr("?", "tenant-a"))
	if !errors.Is(expressionUpdate.Error, ErrTenantFieldUpdate) {
		t.Fatalf("Update(expression tenant field) error = %v, want ErrTenantFieldUpdate", expressionUpdate.Error)
	}

	sameTenantUpdate := db.WithContext(ctx).Model(&tenantOrder{}).Where("id = ?", 1).Update("tenant_id", "tenant-a")
	if sameTenantUpdate.Error != nil {
		t.Fatalf("Update(same tenant field) error = %v", sameTenantUpdate.Error)
	}
	assertSQLContains(t, sameTenantUpdate.Statement.SQL.String(), "tenant_id = ?")
	assertVarsContain(t, sameTenantUpdate.Statement.Vars, "tenant-a")

	sameTenantSave := db.WithContext(ctx).Save(&tenantOrder{ID: 1, TenantID: "tenant-a", Name: "updated"})
	if sameTenantSave.Error != nil {
		t.Fatalf("Save(same tenant field) error = %v", sameTenantSave.Error)
	}
	assertSQLContains(t, sameTenantSave.Statement.SQL.String(), "tenant_id = ?")
	assertVarsContain(t, sameTenantSave.Statement.Vars, "tenant-a")

	hostUpdate := db.WithContext(tenantctx.WithHost(context.Background())).Model(&tenantOrder{}).Where("id = ?", 1).Update("tenant_id", "tenant-b")
	if hostUpdate.Error != nil {
		t.Fatalf("Update(host tenant field) error = %v", hostUpdate.Error)
	}
	assertSQLContains(t, hostUpdate.Statement.SQL.String(), "SET `tenant_id`=?")
}

func TestCountAddsTenantCondition(t *testing.T) {
	db := newDryRunDB(t)
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	var count int64
	tx := db.WithContext(ctx).Model(&tenantOrder{}).Count(&count)
	if tx.Error != nil {
		t.Fatalf("Count() error = %v", tx.Error)
	}
	assertSQLContains(t, tx.Statement.SQL.String(), "tenant_id = ?")
	assertVarsContain(t, tx.Statement.Vars, "tenant-a")
}

func TestRawRequiresHostContext(t *testing.T) {
	db := newDryRunDB(t)
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	tx := db.Session(&gorm.Session{}).WithContext(ctx)
	tx.Statement = &gorm.Statement{DB: tx, Context: tx.Statement.Context}
	New(Config{}).requireHostForRaw(tx)
	if !errors.Is(tx.Error, ErrRawRequiresHost) {
		t.Fatalf("requireHostForRaw(tenant) error = %v, want ErrRawRequiresHost", tx.Error)
	}

	hostTx := db.Session(&gorm.Session{}).WithContext(tenantctx.WithHost(context.Background()))
	hostTx.Statement = &gorm.Statement{DB: hostTx, Context: hostTx.Statement.Context}
	New(Config{}).requireHostForRaw(hostTx)
	if hostTx.Error != nil {
		t.Fatalf("requireHostForRaw(host) error = %v", hostTx.Error)
	}

	var count int64
	err := db.WithContext(ctx).Raw("SELECT COUNT(*) FROM tenant_orders").Scan(&count).Error
	if !errors.Is(err, ErrRawRequiresHost) {
		t.Fatalf("Raw().Scan(tenant) error = %v, want ErrRawRequiresHost", err)
	}
}

func TestSafeRawAndSafeExecRequireHostContext(t *testing.T) {
	db := newDryRunDB(t)
	tenantCtx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	raw := SafeRaw(tenantCtx, db, "SELECT * FROM tenant_orders WHERE tenant_id = ?", "tenant-a")
	if !errors.Is(raw.Error, ErrRawRequiresHost) {
		t.Fatalf("SafeRaw(tenant) error = %v, want ErrRawRequiresHost", raw.Error)
	}

	exec := SafeExec(tenantctx.WithHost(context.Background()), db, "UPDATE tenant_orders SET name = ? WHERE id = ?", "updated", 1)
	if exec.Error != nil {
		t.Fatalf("SafeExec(host) error = %v", exec.Error)
	}

	missing := SafeRaw(context.Background(), db, "SELECT * FROM tenant_orders")
	if !errors.Is(missing.Error, ErrRawRequiresHost) {
		t.Fatalf("SafeRaw(background) error = %v, want ErrRawRequiresHost", missing.Error)
	}
}

func TestTenantScope(t *testing.T) {
	db := newDryRunDB(t)
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	var orders []tenantOrder
	tx := db.WithContext(tenantctx.WithHost(context.Background())).Scopes(TenantScope(ctx)).Find(&orders)
	if tx.Error != nil {
		t.Fatalf("Scopes(TenantScope).Find() error = %v", tx.Error)
	}
	assertSQLContains(t, tx.Statement.SQL.String(), "tenant_id = ?")
	assertVarsContain(t, tx.Statement.Vars, "tenant-a")
}

func TestGuardPreloadsAddsTenantScope(t *testing.T) {
	db := newDryRunDB(t)
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	tx := db.Session(&gorm.Session{}).WithContext(ctx)
	tx.Statement = &gorm.Statement{
		DB:       tx,
		Context:  ctx,
		Preloads: map[string][]interface{}{"Items": {"name <> ?", ""}},
	}

	New(Config{}).guardPreloads(tx)
	if tx.Error != nil {
		t.Fatalf("guardPreloads() error = %v", tx.Error)
	}
	args := tx.Statement.Preloads["Items"]
	if len(args) != 3 {
		t.Fatalf("preload args len = %d, want 3; args=%#v", len(args), args)
	}
	if _, ok := args[2].(func(*gorm.DB) *gorm.DB); !ok {
		t.Fatalf("last preload arg = %T, want tenant scope func", args[2])
	}
}

func TestHardDeleteRequiresHost(t *testing.T) {
	db := newDryRunDB(t)
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	tx := HardDelete(ctx, db, &tenantOrder{}, "id = ?", 1)
	if !errors.Is(tx.Error, saas.ErrHostRequired) {
		t.Fatalf("HardDelete(tenant) error = %v, want ErrHostRequired", tx.Error)
	}

	hostTx := HardDelete(tenantctx.WithHost(context.Background()), db, &tenantOrder{}, "id = ?", 1)
	if hostTx.Error != nil {
		t.Fatalf("HardDelete(host) error = %v", hostTx.Error)
	}
	assertSQLContains(t, hostTx.Statement.SQL.String(), "DELETE FROM")
}

func TestMySQLSoftDeleteUniqueIndex(t *testing.T) {
	index, err := NewMySQLSoftDeleteUniqueIndex("", []string{"email"}, "")
	if err != nil {
		t.Fatalf("NewMySQLSoftDeleteUniqueIndex() error = %v", err)
	}
	want := []string{"tenant_id", "email", "deleted_flag"}
	got := index.Columns()
	if len(got) != len(want) {
		t.Fatalf("Columns() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Columns()[%d] = %q, want %q; all=%#v", i, got[i], want[i], got)
		}
	}

	if _, err := NewMySQLSoftDeleteUniqueIndex("tenant_id;drop", []string{"email"}, "deleted_flag"); !errors.Is(err, data.ErrInvalidFieldName) {
		t.Fatalf("unsafe tenant field error = %v, want ErrInvalidFieldName", err)
	}
	if _, err := NewMySQLSoftDeleteUniqueIndex("tenant_id", nil, "deleted_flag"); !errors.Is(err, data.ErrInvalidFieldName) {
		t.Fatalf("missing business fields error = %v, want ErrInvalidFieldName", err)
	}
}

func newDryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	return newDryRunDBWithConfig(t, Config{})
}

func newDryRunDBWithConfig(t *testing.T, config Config) *gorm.DB {
	t.Helper()

	sqlDB, err := sql.Open("mysql", "gorm:gorm@tcp(localhost:9910)/gorm?charset=utf8&parseTime=True&loc=Local")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
	})

	db, err := gorm.Open(mysql.New(mysql.Config{
		Conn:                      sqlDB,
		DSN:                       "gorm:gorm@tcp(localhost:9910)/gorm?charset=utf8&parseTime=True&loc=Local",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatalf("gorm.Open() error = %v", err)
	}
	if err := db.Use(New(config)); err != nil {
		t.Fatalf("db.Use() error = %v", err)
	}
	return db
}

func assertSQLContains(t *testing.T, sql string, fragment string) {
	t.Helper()
	if !strings.Contains(sql, fragment) {
		t.Fatalf("SQL = %s, want fragment %q", sql, fragment)
	}
}

func assertVarsContain(t *testing.T, vars []any, want string) {
	t.Helper()
	for _, value := range vars {
		if value == want {
			return
		}
	}
	t.Fatalf("Vars = %#v, want value %q", vars, want)
}
