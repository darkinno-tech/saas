package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"

	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/resolver"
	"github.com/darkinno-tech/saas/core/store"
	"github.com/darkinno-tech/saas/core/types"
	gormtenant "github.com/darkinno-tech/saas/data/gorm"
	ginsaas "github.com/darkinno-tech/saas/web/gin"

	"github.com/gin-gonic/gin"
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
	gin.SetMode(gin.ReleaseMode)

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

	router := newRouter(db, tenants)
	request := httptest.NewRequest(http.MethodGet, "/orders", nil)
	request.Header.Set(resolver.DefaultHeaderName, "tenant-a")

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

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

func newRouter(db *gorm.DB, tenants store.Store) *gin.Engine {
	tenantResolver := resolver.NewComposite(
		resolver.NewHeaderContrib("", types.TenantIDStrategyString),
	)

	router := gin.New()
	router.Use(ginsaas.TenantMiddleware(tenantResolver, tenants))
	router.GET("/orders", func(c *gin.Context) {
		tenant, ok := tenantctx.FromContext(c.Request.Context())
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "tenant_required"})
			return
		}

		var orders []Order
		result := db.WithContext(c.Request.Context()).Find(&orders)
		if result.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": result.Error.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"tenant_id": tenant.ID.String(),
			"sql":       result.Statement.SQL.String(),
			"vars":      result.Statement.Vars,
		})
	})
	return router
}
