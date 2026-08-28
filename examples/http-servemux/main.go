package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"

	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/resolver"
	"github.com/darkinno-tech/saas/core/store"
	"github.com/darkinno-tech/saas/core/types"
	gormtenant "github.com/darkinno-tech/saas/data/gorm"
	httpsaas "github.com/darkinno-tech/saas/web/http"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type Order struct {
	ID       uint
	TenantID string `gorm:"column:tenant_id"`
	Number   string `gorm:"column:number"`
}

func (Order) TableName() string {
	return "orders"
}

func main() {
	ctx := context.Background()
	tenants := store.NewMemoryStore()
	if err := tenants.Create(ctx, types.Tenant{
		ID:     "tenant-a",
		Name:   "Tenant A",
		Status: types.TenantStatusActive,
	}); err != nil {
		log.Fatal(err)
	}

	db, err := newDryRunDB()
	if err != nil {
		log.Fatal(err)
	}

	handler := newHandler(db, tenants)
	request := httptest.NewRequest(http.MethodGet, "/orders", nil)
	request.Header.Set(resolver.DefaultHeaderName, "tenant-a")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	fmt.Println(recorder.Code)
	fmt.Println(recorder.Body.String())
}

func newDryRunDB() (*gorm.DB, error) {
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "user:pass@tcp(localhost:3306)/app?parseTime=true",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{
		DryRun:                 true,
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
	})
	if err != nil {
		return nil, err
	}

	if err := db.Use(gormtenant.New(gormtenant.Config{})); err != nil {
		return nil, err
	}
	return db, nil
}

func newHandler(db *gorm.DB, tenants store.Store) http.Handler {
	tenantResolver := resolver.NewComposite(
		resolver.NewHeaderContrib("", types.TenantIDStrategyString),
	)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /orders", func(w http.ResponseWriter, r *http.Request) {
		tenant, ok := tenantctx.FromContext(r.Context())
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "tenant_required"})
			return
		}

		var orders []Order
		result := db.WithContext(r.Context()).Find(&orders)
		if result.Error != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": result.Error.Error()})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"tenant_id": tenant.ID.String(),
			"sql":       result.Statement.SQL.String(),
			"vars":      result.Statement.Vars,
		})
	})

	return httpsaas.TenantMiddleware(tenantResolver, tenants)(mux)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
