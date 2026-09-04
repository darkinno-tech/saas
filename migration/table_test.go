package migration

import "testing"

func TestCreateTable(t *testing.T) {
	tests := []struct {
		name       string
		columns    []Column
		uniqueKeys [][]string
		want       string
		wantErr    bool
	}{
		{
			name: "plain columns",
			columns: []Column{
				{Name: "id", Type: "BIGINT", NotNull: true},
				{Name: "tenant_id", Type: "VARCHAR(64)", NotNull: true},
				{Name: "used", Type: "BIGINT", NotNull: true, Default: "0"},
			},
			uniqueKeys: [][]string{{"tenant_id", "resource"}},
			want:       "CREATE TABLE ai_usage (id BIGINT NOT NULL, tenant_id VARCHAR(64) NOT NULL, used BIGINT NOT NULL DEFAULT 0, UNIQUE (tenant_id, resource))",
		},
		{
			name:       "empty columns rejected",
			columns:    nil,
			uniqueKeys: nil,
			wantErr:    true,
		},
		{
			name: "unsafe identifier rejected",
			columns: []Column{
				{Name: "bad;name", Type: "BIGINT"},
			},
			wantErr: true,
		},
		{
			name: "unsafe type rejected",
			columns: []Column{
				{Name: "id", Type: "BIGINT;DROP"},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			planner := NewPlanner(DialectSQLite)
			got, err := planner.CreateTable("ai_usage", tt.columns, tt.uniqueKeys)
			if (err != nil) != tt.wantErr {
				t.Fatalf("CreateTable() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got != tt.want {
				t.Fatalf("CreateTable() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCreateTablePostgresPlacementIndependent(t *testing.T) {
	planner := NewPlanner(DialectPostgres)
	got, err := planner.CreateTable("ai_usage", []Column{
		{Name: "tenant_id", Type: "VARCHAR(64)", NotNull: true},
	}, [][]string{{"tenant_id"}})
	if err != nil {
		t.Fatal(err)
	}
	want := "CREATE TABLE ai_usage (tenant_id VARCHAR(64) NOT NULL, UNIQUE (tenant_id))"
	if got != want {
		t.Fatalf("CreateTable() = %q, want %q", got, want)
	}
}