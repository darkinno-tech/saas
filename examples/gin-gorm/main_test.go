package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/im10furry/saas/core/resolver"
	"github.com/im10furry/saas/core/store"
	"github.com/im10furry/saas/core/types"
)

func TestGinGORMExampleRouter(t *testing.T) {
	db, err := newDryRunDB()
	if err != nil {
		t.Fatalf("newDryRunDB() error = %v", err)
	}
	tenants := store.NewMemoryStore()
	if err := tenants.Create(context.Background(), types.Tenant{ID: "tenant-a", Name: "Tenant A", Status: types.TenantStatusActive}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/orders", nil)
	request.Header.Set(resolver.DefaultHeaderName, "tenant-a")
	recorder := httptest.NewRecorder()
	newRouter(db, tenants).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /orders status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		TenantID string   `json:"tenant_id"`
		SQL      string   `json:"sql"`
		Vars     []string `json:"vars"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("GET /orders response is not JSON: %v; body = %s", err, recorder.Body.String())
	}
	if payload.TenantID != "tenant-a" {
		t.Fatalf("GET /orders tenant_id = %q, want tenant-a", payload.TenantID)
	}
	whereIndex := strings.Index(strings.ToLower(payload.SQL), "where")
	if whereIndex < 0 || !strings.Contains(strings.ToLower(payload.SQL[whereIndex:]), "tenant_id") {
		t.Fatalf("GET /orders SQL = %q, want tenant_id predicate in WHERE clause", payload.SQL)
	}
	if len(payload.Vars) != 1 || payload.Vars[0] != "tenant-a" {
		t.Fatalf("GET /orders SQL vars = %v, want only tenant-a", payload.Vars)
	}
}

func TestGinGORMExampleRouterRejectsInvalidTenantRequests(t *testing.T) {
	db, err := newDryRunDB()
	if err != nil {
		t.Fatalf("newDryRunDB() error = %v", err)
	}
	tenants := store.NewMemoryStore()
	if err := tenants.Create(context.Background(), types.Tenant{ID: "tenant-suspended", Name: "Suspended", Status: types.TenantStatusSuspended}); err != nil {
		t.Fatalf("Create(suspended) error = %v", err)
	}
	router := newRouter(db, tenants)

	tests := []struct {
		name     string
		tenantID string
		wantCode int
		wantBody string
	}{
		{name: "missing tenant header", wantCode: http.StatusUnauthorized, wantBody: "tenant_required"},
		{name: "unknown tenant", tenantID: "tenant-missing", wantCode: http.StatusForbidden, wantBody: "tenant_forbidden"},
		{name: "inactive tenant", tenantID: "tenant-suspended", wantCode: http.StatusForbidden, wantBody: "tenant_inactive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/orders", nil)
			if tt.tenantID != "" {
				request.Header.Set(resolver.DefaultHeaderName, tt.tenantID)
			}
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != tt.wantCode {
				t.Fatalf("GET /orders status = %d, want %d; body = %s", recorder.Code, tt.wantCode, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), tt.wantBody) {
				t.Fatalf("GET /orders body = %q, want %q", recorder.Body.String(), tt.wantBody)
			}
		})
	}
}
