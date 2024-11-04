package dbtest_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqltype"
	"github.com/uptrace/bun/migrate"
	"github.com/uptrace/bun/migrate/sqlschema"
)

const (
	migrationsTable     = "test_migrations"
	migrationLocksTable = "test_migration_locks"
)

var migrationsDir = filepath.Join(os.TempDir(), "dbtest")

// cleanupMigrations adds a cleanup function to reset migration tables.
// The reset does not run for skipped tests to avoid unnecessary work.
//
// Usage:
//
//	testEachDB(t, func(t *testing.T, dbName string, db *bun.DB) {
//		cleanupMigrations(t, ctx, db)
//		// some test that may generate migration entries in the db
//	})
func cleanupMigrations(tb testing.TB, ctx context.Context, db *bun.DB) {
	tb.Cleanup(func() {
		if tb.Skipped() {
			return
		}

		m := migrate.NewMigrator(db, migrate.NewMigrations(),
			migrate.WithTableName(migrationsTable),
			migrate.WithLocksTableName(migrationLocksTable),
		)
		require.NoError(tb, m.Reset(ctx))
	})
}

func TestMigrate(t *testing.T) {
	type Test struct {
		run func(t *testing.T, db *bun.DB)
	}

	tests := []Test{
		{run: testMigrateUpAndDown},
		{run: testMigrateUpError},
	}

	testEachDB(t, func(t *testing.T, dbName string, db *bun.DB) {
		cleanupMigrations(t, ctx, db)

		for _, test := range tests {
			t.Run(funcName(test.run), func(t *testing.T) {
				test.run(t, db)
			})
		}
	})
}

func testMigrateUpAndDown(t *testing.T, db *bun.DB) {
	ctx := context.Background()

	var history []string

	migrations := migrate.NewMigrations()
	migrations.Add(migrate.Migration{
		Name: "20060102150405",
		Up: func(ctx context.Context, db *bun.DB) error {
			history = append(history, "up1")
			return nil
		},
		Down: func(ctx context.Context, db *bun.DB) error {
			history = append(history, "down1")
			return nil
		},
	})
	migrations.Add(migrate.Migration{
		Name: "20060102160405",
		Up: func(ctx context.Context, db *bun.DB) error {
			history = append(history, "up2")
			return nil
		},
		Down: func(ctx context.Context, db *bun.DB) error {
			history = append(history, "down2")
			return nil
		},
	})

	m := migrate.NewMigrator(db, migrations,
		migrate.WithTableName(migrationsTable),
		migrate.WithLocksTableName(migrationLocksTable),
	)
	err := m.Reset(ctx)
	require.NoError(t, err)

	group, err := m.Migrate(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), group.ID)
	require.Len(t, group.Migrations, 2)
	require.Equal(t, []string{"up1", "up2"}, history)

	history = nil
	group, err = m.Rollback(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), group.ID)
	require.Len(t, group.Migrations, 2)
	require.Equal(t, []string{"down2", "down1"}, history)
}

func testMigrateUpError(t *testing.T, db *bun.DB) {
	ctx := context.Background()

	var history []string

	migrations := migrate.NewMigrations()
	migrations.Add(migrate.Migration{
		Name: "20060102150405",
		Up: func(ctx context.Context, db *bun.DB) error {
			history = append(history, "up1")
			return nil
		},
		Down: func(ctx context.Context, db *bun.DB) error {
			history = append(history, "down1")
			return nil
		},
	})
	migrations.Add(migrate.Migration{
		Name: "20060102160405",
		Up: func(ctx context.Context, db *bun.DB) error {
			history = append(history, "up2")
			return errors.New("failed")
		},
		Down: func(ctx context.Context, db *bun.DB) error {
			history = append(history, "down2")
			return nil
		},
	})
	migrations.Add(migrate.Migration{
		Name: "20060102170405",
		Up: func(ctx context.Context, db *bun.DB) error {
			history = append(history, "up3")
			return errors.New("failed")
		},
		Down: func(ctx context.Context, db *bun.DB) error {
			history = append(history, "down3")
			return nil
		},
	})

	m := migrate.NewMigrator(db, migrations,
		migrate.WithTableName(migrationsTable),
		migrate.WithLocksTableName(migrationLocksTable),
	)
	err := m.Reset(ctx)
	require.NoError(t, err)

	group, err := m.Migrate(ctx)
	require.Error(t, err)
	require.Equal(t, "failed", err.Error())
	require.Equal(t, int64(1), group.ID)
	require.Len(t, group.Migrations, 2)
	require.Equal(t, []string{"up1", "up2"}, history)

	history = nil
	group, err = m.Rollback(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), group.ID)
	require.Len(t, group.Migrations, 2)
	require.Equal(t, []string{"down2", "down1"}, history)
}

// newAutoMigratorOrSkip creates an AutoMigrator configured to use test migratins/locks
// tables and dedicated migrations directory. If an AutoMigrator cannob be created because
// the dialect doesn't support either schema inspections or migrations, the test will be *skipped*
// with the corresponding error.
// Additionally, it will create the migrations directory and if
// one does not exist and add a function to tear it down on cleanup.
func newAutoMigratorOrSkip(tb testing.TB, db *bun.DB, opts ...migrate.AutoMigratorOption) *migrate.AutoMigrator {
	tb.Helper()

	opts = append(opts,
		migrate.WithTableNameAuto(migrationsTable),
		migrate.WithLocksTableNameAuto(migrationLocksTable),
		migrate.WithMigrationsDirectoryAuto(migrationsDir),
	)

	m, err := migrate.NewAutoMigrator(db, opts...)
	if err != nil {
		tb.Skip(err)
	}

	err = os.MkdirAll(migrationsDir, os.ModePerm)
	require.NoError(tb, err, "cannot continue test without migrations directory")

	tb.Cleanup(func() {
		if err := os.RemoveAll(migrationsDir); err != nil {
			tb.Logf("cleanup: remove migrations dir: %v", err)
		}
	})

	return m
}

// inspectDbOrSkip returns a function to inspect the current state of the database.
// The test will be *skipped* if the current dialect doesn't support database inpection
// and fail if the inspector cannot successfully retrieve database state.
func inspectDbOrSkip(tb testing.TB, db *bun.DB) func(context.Context) sqlschema.State {
	tb.Helper()
	// AutoMigrator excludes these tables by default, but here we need to do this explicitly.
	inspector, err := sqlschema.NewInspector(db, migrationsTable, migrationLocksTable)
	if err != nil {
		tb.Skip(err)
	}
	return func(ctx context.Context) sqlschema.State {
		state, err := inspector.Inspect(ctx)
		require.NoError(tb, err)
		return state
	}
}

func TestAutoMigrator_CreateSQLMigrations(t *testing.T) {
	type NewTable struct {
		bun.BaseModel `bun:"table:new_table"`
		Bar           string
		Baz           time.Time
	}

	testEachDB(t, func(t *testing.T, dbName string, db *bun.DB) {
		ctx := context.Background()
		m := newAutoMigratorOrSkip(t, db, migrate.WithModel((*NewTable)(nil)))

		migrations, err := m.CreateSQLMigrations(ctx)
		require.NoError(t, err, "should create migrations successfully")

		require.Len(t, migrations, 2, "expected up/down migration pair")
		require.DirExists(t, migrationsDir)
		checkMigrationFileContains(t, ".up.sql", "CREATE TABLE")
		checkMigrationFileContains(t, ".down.sql", "DROP TABLE")
	})
}

// checkMigrationFileContains expected SQL snippet.
func checkMigrationFileContains(t *testing.T, fileSuffix string, content string) {
	t.Helper()

	files, err := os.ReadDir(migrationsDir)
	require.NoErrorf(t, err, "list files in %s", migrationsDir)

	for _, f := range files {
		if strings.HasSuffix(f.Name(), fileSuffix) {
			b, err := os.ReadFile(filepath.Join(migrationsDir, f.Name()))
			require.NoError(t, err)
			require.Containsf(t, string(b), content, "expected %s file to contain string", f.Name())
			return
		}
	}
	t.Errorf("no *%s file in migrations directory (%s)", fileSuffix, migrationsDir)
}

// checkMigrationFilesExist makes sure both up- and down- SQL migration files were created.
func checkMigrationFilesExist(t *testing.T) {
	t.Helper()

	files, err := os.ReadDir(migrationsDir)
	require.NoErrorf(t, err, "list files in %s", migrationsDir)

	var up, down bool
	for _, f := range files {
		if !up && strings.HasSuffix(f.Name(), ".up.sql") {
			up = true
		} else if !down && strings.HasSuffix(f.Name(), ".down.sql") {
			down = true
		}
	}

	if !up {
		t.Errorf("no .up.sql file created in migrations directory (%s)", migrationsDir)
	}
	if !down {
		t.Errorf("no .down.sql file created in migrations directory (%s)", migrationsDir)
	}
}

func runMigrations(t *testing.T, m *migrate.AutoMigrator) {
	t.Helper()

	_, err := m.Migrate(ctx)
	require.NoError(t, err, "auto migration failed")
	checkMigrationFilesExist(t)
}

func TestAutoMigrator_Migrate(t *testing.T) {

	tests := []struct {
		fn func(t *testing.T, db *bun.DB)
	}{
		// {testRenameTable},
		// {testRenamedColumns},
		{testCreateDropTable},
		// {testAlterForeignKeys},
		// {testChangeColumnType_AutoCast},
		// {testIdentity},
		// {testAddDropColumn},
		// {testUnique},
		// {testUniqueRenamedTable},
		// {testUpdatePrimaryKeys},

		// Suspended support for renaming foreign keys:
		// {testCustomFKNameFunc},
		// {testForceRenameFK},
		// {testRenameColumnRenamesFK},
	}

	testEachDB(t, func(t *testing.T, dbName string, db *bun.DB) {
		for _, tt := range tests {
			t.Run(funcName(tt.fn), func(t *testing.T) {
				// Because they are executed so fast, tests may generate migrations
				// with the same timestamp, so that only the first of them will apply.
				// To eliminate these side-effects we cleanup migration tables after
				// after every test case.
				cleanupMigrations(t, ctx, db)
				tt.fn(t, db)
			})
		}
	})
}

func testRenameTable(t *testing.T, db *bun.DB) {
	type initial struct {
		bun.BaseModel `bun:"table:initial"`
		Foo           int `bun:"foo,notnull"`
	}

	type changed struct {
		bun.BaseModel `bun:"table:changed"`
		Foo           int `bun:"foo,notnull"`
	}

	// Arrange
	ctx := context.Background()
	inspect := inspectDbOrSkip(t, db)
	mustResetModel(t, ctx, db, (*initial)(nil))
	mustDropTableOnCleanup(t, ctx, db, (*changed)(nil))
	m := newAutoMigratorOrSkip(t, db, migrate.WithModel((*changed)(nil)))

	// Act
	runMigrations(t, m)

	// Assert
	state := inspect(ctx)
	tables := state.Tables
	require.Len(t, tables, 1)
	require.Equal(t, "changed", tables[0].Name)
}

func testCreateDropTable(t *testing.T, db *bun.DB) {
	type DropMe struct {
		bun.BaseModel `bun:"table:dropme"`
		Foo           int `bun:"foo,identity"`
	}

	type CreateMe struct {
		bun.BaseModel `bun:"table:createme"`
		Bar           string `bun:",pk,default:gen_random_uuid()"`
		Baz           time.Time
	}

	// Arrange
	ctx := context.Background()
	inspect := inspectDbOrSkip(t, db)
	mustResetModel(t, ctx, db, (*DropMe)(nil))
	mustDropTableOnCleanup(t, ctx, db, (*CreateMe)(nil))
	m := newAutoMigratorOrSkip(t, db, migrate.WithModel((*CreateMe)(nil)))

	// Act
	runMigrations(t, m)

	// Assert
	state := inspect(ctx)
	tables := state.Tables
	require.Len(t, tables, 1)
	require.Equal(t, "createme", tables[0].Name)
}

func testAlterForeignKeys(t *testing.T, db *bun.DB) {
	// Initial state -- each thing has one owner
	type OwnerExclusive struct {
		bun.BaseModel `bun:"owners"`
		ID            int64 `bun:",pk"`
	}

	type ThingExclusive struct {
		bun.BaseModel `bun:"things"`
		ID            int64 `bun:",pk"`
		OwnerID       int64 `bun:",notnull"`

		Owner *OwnerExclusive `bun:"rel:belongs-to,join:owner_id=id"`
	}

	// Change -- each thing has multiple owners

	type ThingCommon struct {
		bun.BaseModel `bun:"things"`
		ID            int64 `bun:",pk"`
	}

	type OwnerCommon struct {
		bun.BaseModel `bun:"owners"`
		ID            int64          `bun:",pk"`
		Things        []*ThingCommon `bun:"m2m:things_to_owners,join:Owner=Thing"`
	}

	type ThingsToOwner struct {
		OwnerID int64        `bun:",notnull"`
		Owner   *OwnerCommon `bun:"rel:belongs-to,join:owner_id=id"`
		ThingID int64        `bun:",notnull"`
		Thing   *ThingCommon `bun:"rel:belongs-to,join:thing_id=id"`
	}

	// Arrange
	ctx := context.Background()
	inspect := inspectDbOrSkip(t, db)
	db.RegisterModel((*ThingsToOwner)(nil))

	mustCreateTableWithFKs(t, ctx, db,
		(*OwnerExclusive)(nil),
		(*ThingExclusive)(nil),
	)
	mustDropTableOnCleanup(t, ctx, db, (*ThingsToOwner)(nil))

	m := newAutoMigratorOrSkip(t, db, migrate.WithModel(
		(*ThingCommon)(nil),
		(*OwnerCommon)(nil),
		(*ThingsToOwner)(nil),
	))

	// Act
	runMigrations(t, m)

	// Assert
	state := inspect(ctx)
	defaultSchema := db.Dialect().DefaultSchema()

	// Crated 2 new constraints
	require.Contains(t, state.FKs, sqlschema.FK{
		From: sqlschema.C(defaultSchema, "things_to_owners", "owner_id"),
		To:   sqlschema.C(defaultSchema, "owners", "id"),
	})
	require.Contains(t, state.FKs, sqlschema.FK{
		From: sqlschema.C(defaultSchema, "things_to_owners", "thing_id"),
		To:   sqlschema.C(defaultSchema, "things", "id"),
	})

	// Dropped the initial one
	require.NotContains(t, state.FKs, sqlschema.FK{
		From: sqlschema.C(defaultSchema, "things", "owner_id"),
		To:   sqlschema.C(defaultSchema, "owners", "id"),
	})
}

func testForceRenameFK(t *testing.T, db *bun.DB) {
	// Database state
	type Owner struct {
		ID int64 `bun:",pk"`
	}

	type OwnedThing struct {
		bun.BaseModel `bun:"table:things"`
		ID            int64 `bun:",pk"`
		OwnerID       int64 `bun:"owner_id,notnull"`

		Owner *Owner `bun:"rel:belongs-to,join:owner_id=id"`
	}

	// Model state
	type Person struct {
		ID int64 `bun:",pk"`
	}

	type PersonalThing struct {
		bun.BaseModel `bun:"table:things"`
		ID            int64 `bun:",pk"`
		PersonID      int64 `bun:"owner_id,notnull"`

		Owner *Person `bun:"rel:belongs-to,join:owner_id=id"`
	}

	ctx := context.Background()
	inspect := inspectDbOrSkip(t, db)

	mustCreateTableWithFKs(t, ctx, db,
		(*Owner)(nil),
		(*OwnedThing)(nil),
	)
	mustDropTableOnCleanup(t, ctx, db, (*Person)(nil))

	m := newAutoMigratorOrSkip(t, db,
		migrate.WithModel(
			(*Person)(nil),
			(*PersonalThing)(nil),
		),
		migrate.WithRenameFK(true),
		migrate.WithFKNameFunc(func(fk sqlschema.FK) string {
			return strings.Join([]string{
				fk.From.Table, fk.To.Table, "fkey",
			}, "_")
		}),
	)

	// Act
	runMigrations(t, m)

	// Assert
	state := inspect(ctx)
	schema := db.Dialect().DefaultSchema()
	wantName, ok := state.FKs[sqlschema.FK{
		From: sqlschema.C(schema, "things", "owner_id"),
		To:   sqlschema.C(schema, "people", "id"),
	}]
	require.True(t, ok, "expect state.FKs to contain things_people_fkey")
	require.Equal(t, wantName, "things_people_fkey")
}

func testCustomFKNameFunc(t *testing.T, db *bun.DB) {
	// Database state
	type Column struct {
		OID   int64 `bun:",pk"`
		RelID int64 `bun:"attrelid,notnull"`
	}
	type Table struct {
		OID int64 `bun:",pk"`
	}

	// Model state
	type ColumnM struct {
		bun.BaseModel `bun:"table:columns"`
		OID           int64 `bun:",pk"`
		RelID         int64 `bun:"attrelid,notnull"`

		Table *Table `bun:"rel:belongs-to,join:attrelid=oid"`
	}
	type TableM struct {
		bun.BaseModel `bun:"table:tables"`
		OID           int64 `bun:",pk"`
	}

	ctx := context.Background()
	inspect := inspectDbOrSkip(t, db)

	mustCreateTableWithFKs(t, ctx, db,
		(*Table)(nil),
		(*Column)(nil),
	)

	m := newAutoMigratorOrSkip(t, db,
		migrate.WithFKNameFunc(func(sqlschema.FK) string { return "test_fkey" }),
		migrate.WithModel(
			(*TableM)(nil),
			(*ColumnM)(nil),
		),
	)

	// Act
	runMigrations(t, m)

	// Assert
	state := inspect(ctx)
	fkName := state.FKs[sqlschema.FK{
		From: sqlschema.C(db.Dialect().DefaultSchema(), "columns", "attrelid"),
		To:   sqlschema.C(db.Dialect().DefaultSchema(), "tables", "oid"),
	}]
	require.Equal(t, "test_fkey", fkName)
}

func testRenamedColumns(t *testing.T, db *bun.DB) {
	// Database state
	type Original struct {
		bun.BaseModel `bun:"original"`
		ID            int64 `bun:"id,pk"`
	}

	type Model1 struct {
		bun.BaseModel `bun:"models"`
		ID            string `bun:",pk"`
		DoNotRename   string `bun:",default:2"`
		ColumnTwo     int    `bun:",default:2"`
	}

	// Model state
	type Renamed struct {
		bun.BaseModel `bun:"renamed"`
		Count         int64 `bun:"count,pk"` // renamed column in renamed model
	}

	type Model2 struct {
		bun.BaseModel `bun:"models"`
		ID            string `bun:",pk"`
		DoNotRename   string `bun:",default:2"`
		SecondColumn  int    `bun:",default:2"` // renamed column
	}

	ctx := context.Background()
	inspect := inspectDbOrSkip(t, db)
	mustResetModel(t, ctx, db,
		(*Original)(nil),
		(*Model1)(nil),
	)
	mustDropTableOnCleanup(t, ctx, db, (*Renamed)(nil))
	m := newAutoMigratorOrSkip(t, db, migrate.WithModel(
		(*Model2)(nil),
		(*Renamed)(nil),
	))

	// Act
	runMigrations(t, m)

	// Assert
	state := inspect(ctx)
	require.Len(t, state.Tables, 2)

	var renamed, model2 sqlschema.Table
	for _, tbl := range state.Tables {
		switch tbl.Name {
		case "renamed":
			renamed = tbl
		case "models":
			model2 = tbl
		}
	}

	require.Contains(t, renamed.Columns, "count")
	require.Contains(t, model2.Columns, "second_column")
	require.Contains(t, model2.Columns, "do_not_rename")
}

func testRenameColumnRenamesFK(t *testing.T, db *bun.DB) {
	type TennantBefore struct {
		bun.BaseModel `bun:"table:tennants"`
		ID            int64 `bun:"id,pk,identity"`
		Apartment     int8
		NeighbourID   int64 `bun:"neighbour_id"`

		Neighbour *TennantBefore `bun:"rel:has-one,join:neighbour_id=id"`
	}

	type TennantAfter struct {
		bun.BaseModel `bun:"table:tennants"`
		TennantID     int64 `bun:"tennant_id,pk,identity"`
		Apartment     int8
		NeighbourID   int64 `bun:"my_neighbour"`

		Neighbour *TennantAfter `bun:"rel:has-one,join:my_neighbour=tennant_id"`
	}

	ctx := context.Background()
	inspect := inspectDbOrSkip(t, db)
	mustCreateTableWithFKs(t, ctx, db, (*TennantBefore)(nil))
	m := newAutoMigratorOrSkip(t, db,
		migrate.WithRenameFK(true),
		migrate.WithModel((*TennantAfter)(nil)),
	)

	// Act
	runMigrations(t, m)

	// Assert
	state := inspect(ctx)
	fkName := state.FKs[sqlschema.FK{
		From: sqlschema.C(db.Dialect().DefaultSchema(), "tennants", "my_neighbour"),
		To:   sqlschema.C(db.Dialect().DefaultSchema(), "tennants", "tennant_id"),
	}]
	require.Equal(t, "tennants_my_neighbour_fkey", fkName)
}

// testChangeColumnType_AutoCast checks type changes which can be type-casted automatically,
// i.e. do not require supplying a USING clause (pgdialect).
func testChangeColumnType_AutoCast(t *testing.T, db *bun.DB) {
	type TableBefore struct {
		bun.BaseModel `bun:"table:table"`

		SmallInt     int32     `bun:"bigger_int,pk,identity"`
		Timestamp    time.Time `bun:"ts"`
		DefaultExpr  string    `bun:"default_expr,default:gen_random_uuid()"`
		EmptyDefault string    `bun:"empty_default"`
		Nullable     string    `bun:"not_null"`
		TypeOverride string    `bun:"type:varchar(100)"`
		// ManyValues    []string  `bun:",array"`
	}

	type TableAfter struct {
		bun.BaseModel `bun:"table:table"`

		BigInt       int64     `bun:"bigger_int,pk,identity"`        // int64 maps to bigint
		Timestamp    time.Time `bun:"ts,default:current_timestamp"`  // has default value now
		DefaultExpr  string    `bun:"default_expr,default:random()"` // different default
		EmptyDefault string    `bun:"empty_default,default:''"`      // '' empty string default
		NotNullable  string    `bun:"not_null,notnull"`              // added NOT NULL
		TypeOverride string    `bun:"type:varchar(200)"`             // new length
		// ManyValues    []string  `bun:",array"`                    // did not change
	}

	wantTables := []sqlschema.Table{
		{
			Schema: db.Dialect().DefaultSchema(),
			Name:   "table",
			Columns: map[string]sqlschema.Column{
				"bigger_int": {
					SQLType:    "bigint",
					IsIdentity: true,
				},
				"ts": {
					SQLType:      "timestamp",         // FIXME(dyma): convert "timestamp with time zone" to sqltype.Timestamp
					DefaultValue: "current_timestamp", // FIXME(dyma): Convert driver-specific value to common "expressions" (e.g. CURRENT_TIMESTAMP == current_timestamp) OR lowercase all types.
					IsNullable:   true,
				},
				"default_expr": {
					SQLType:      "varchar",
					IsNullable:   true,
					DefaultValue: "random()",
				},
				"empty_default": {
					SQLType:      "varchar",
					IsNullable:   true,
					DefaultValue: "", // NOT "''"
				},
				"not_null": {
					SQLType:    "varchar",
					IsNullable: false,
				},
				"type_override": {
					SQLType:    "varchar",
					IsNullable: true,
					VarcharLen: 200,
				},
				// "many_values": {
				// 	SQLType: "array",
				// },
			},
			PK: &sqlschema.PK{Columns: sqlschema.NewComposite("bigger_int")},
		},
	}

	ctx := context.Background()
	inspect := inspectDbOrSkip(t, db)
	mustResetModel(t, ctx, db, (*TableBefore)(nil))
	m := newAutoMigratorOrSkip(t, db, migrate.WithModel((*TableAfter)(nil)))

	// Act
	runMigrations(t, m)

	// Assert
	state := inspect(ctx)
	cmpTables(t, db.Dialect().(sqlschema.InspectorDialect), wantTables, state.Tables)
}

func testIdentity(t *testing.T, db *bun.DB) {
	type TableBefore struct {
		bun.BaseModel `bun:"table:table"`
		A             int64 `bun:",notnull,identity"`
		B             int64
	}

	type TableAfter struct {
		bun.BaseModel `bun:"table:table"`
		A             int64 `bun:",notnull"`
		B             int64 `bun:",notnull,identity"`
	}

	wantTables := []sqlschema.Table{
		{
			Schema: db.Dialect().DefaultSchema(),
			Name:   "table",
			Columns: map[string]sqlschema.Column{
				"a": {
					SQLType:    sqltype.BigInt,
					IsIdentity: false, // <- drop IDENTITY
				},
				"b": {
					SQLType:    sqltype.BigInt,
					IsIdentity: true, // <- add IDENTITY
				},
			},
		},
	}

	ctx := context.Background()
	inspect := inspectDbOrSkip(t, db)
	mustResetModel(t, ctx, db, (*TableBefore)(nil))
	m := newAutoMigratorOrSkip(t, db, migrate.WithModel((*TableAfter)(nil)))

	// Act
	runMigrations(t, m)

	// Assert
	state := inspect(ctx)
	cmpTables(t, db.Dialect().(sqlschema.InspectorDialect), wantTables, state.Tables)
}

func testAddDropColumn(t *testing.T, db *bun.DB) {
	type TableBefore struct {
		bun.BaseModel `bun:"table:table"`
		DoNotTouch    string `bun:"do_not_touch"`
		DropMe        string `bun:"dropme"`
	}

	type TableAfter struct {
		bun.BaseModel `bun:"table:table"`
		DoNotTouch    string `bun:"do_not_touch"`
		AddMe         bool   `bun:"addme"`
	}

	wantTables := []sqlschema.Table{
		{
			Schema: db.Dialect().DefaultSchema(),
			Name:   "table",
			Columns: map[string]sqlschema.Column{
				"do_not_touch": {
					SQLType:    sqltype.VarChar,
					IsNullable: true,
				},
				"addme": {
					SQLType:    sqltype.Boolean,
					IsNullable: true,
				},
			},
		},
	}

	ctx := context.Background()
	inspect := inspectDbOrSkip(t, db)
	mustResetModel(t, ctx, db, (*TableBefore)(nil))
	m := newAutoMigratorOrSkip(t, db, migrate.WithModel((*TableAfter)(nil)))

	// Act
	runMigrations(t, m)

	// Assert
	state := inspect(ctx)
	cmpTables(t, db.Dialect().(sqlschema.InspectorDialect), wantTables, state.Tables)
}

func testUnique(t *testing.T, db *bun.DB) {
	type TableBefore struct {
		bun.BaseModel `bun:"table:table"`
		FirstName     string `bun:"first_name,unique:full_name"`
		LastName      string `bun:"last_name,unique:full_name"`
		Birthday      string `bun:"birthday,unique"`
		PetName       string `bun:"pet_name,unique:pet"`
		PetBreed      string `bun:"pet_breed,unique:pet"`
	}

	type TableAfter struct {
		bun.BaseModel `bun:"table:table"`
		FirstName     string `bun:"first_name,unique:full_name"`
		MiddleName    string `bun:"middle_name,unique:full_name"` // extend "full_name" unique group
		LastName      string `bun:"last_name,unique:full_name"`

		Birthday string `bun:"birthday"`     // doesn't have to be unique any more
		Email    string `bun:"email,unique"` // new column, unique

		PetName  string `bun:"pet_name,unique"`
		PetBreed string `bun:"pet_breed"` // shrink "pet" unique group
	}

	wantTables := []sqlschema.Table{
		{
			Schema: db.Dialect().DefaultSchema(),
			Name:   "table",
			Columns: map[string]sqlschema.Column{
				"first_name": {
					SQLType:    sqltype.VarChar,
					IsNullable: true,
				},
				"middle_name": {
					SQLType:    sqltype.VarChar,
					IsNullable: true,
				},
				"last_name": {
					SQLType:    sqltype.VarChar,
					IsNullable: true,
				},
				"birthday": {
					SQLType:    sqltype.VarChar,
					IsNullable: true,
				},
				"email": {
					SQLType:    sqltype.VarChar,
					IsNullable: true,
				},
				"pet_name": {
					SQLType:    sqltype.VarChar,
					IsNullable: true,
				},
				"pet_breed": {
					SQLType:    sqltype.VarChar,
					IsNullable: true,
				},
			},
			UniqueContraints: []sqlschema.Unique{
				{Columns: sqlschema.NewComposite("email")},
				{Columns: sqlschema.NewComposite("pet_name")},
				// We can only be sure of the user-defined index name
				{Name: "full_name", Columns: sqlschema.NewComposite("first_name", "middle_name", "last_name")},
			},
		},
	}

	ctx := context.Background()
	inspect := inspectDbOrSkip(t, db)
	mustResetModel(t, ctx, db, (*TableBefore)(nil))
	m := newAutoMigratorOrSkip(t, db, migrate.WithModel((*TableAfter)(nil)))

	// Act
	runMigrations(t, m)

	// Assert
	state := inspect(ctx)
	cmpTables(t, db.Dialect().(sqlschema.InspectorDialect), wantTables, state.Tables)
}

func testUniqueRenamedTable(t *testing.T, db *bun.DB) {
	type TableBefore struct {
		bun.BaseModel `bun:"table:before"`
		FirstName     string `bun:"first_name,unique:full_name"`
		LastName      string `bun:"last_name,unique:full_name"`
		Birthday      string `bun:"birthday,unique"`
		PetName       string `bun:"pet_name,unique:pet"`
		PetBreed      string `bun:"pet_breed,unique:pet"`
	}

	type TableAfter struct {
		bun.BaseModel `bun:"table:after"`
		// Expand full_name unique group and rename it.
		FirstName string `bun:"first_name,unique:birth_certificate"`
		LastName  string `bun:"last_name,unique:birth_certificate"`
		Birthday  string `bun:"birthday,unique:birth_certificate"`

		// pet_name and pet_breed have their own unique indices now.
		PetName  string `bun:"pet_name,unique"`
		PetBreed string `bun:"pet_breed,unique"`
	}

	wantTables := []sqlschema.Table{
		{
			Schema: db.Dialect().DefaultSchema(),
			Name:   "after",
			Columns: map[string]sqlschema.Column{
				"first_name": {
					SQLType:    sqltype.VarChar,
					IsNullable: true,
				},
				"last_name": {
					SQLType:    sqltype.VarChar,
					IsNullable: true,
				},
				"birthday": {
					SQLType:    sqltype.VarChar,
					IsNullable: true,
				},
				"pet_name": {
					SQLType:    sqltype.VarChar,
					IsNullable: true,
				},
				"pet_breed": {
					SQLType:    sqltype.VarChar,
					IsNullable: true,
				},
			},
			UniqueContraints: []sqlschema.Unique{
				{Columns: sqlschema.NewComposite("pet_name")},
				{Columns: sqlschema.NewComposite("pet_breed")},
				{Name: "full_name", Columns: sqlschema.NewComposite("first_name", "last_name", "birthday")},
			},
		},
	}

	ctx := context.Background()
	inspect := inspectDbOrSkip(t, db)
	mustResetModel(t, ctx, db, (*TableBefore)(nil))
	mustDropTableOnCleanup(t, ctx, db, (*TableAfter)(nil))
	m := newAutoMigratorOrSkip(t, db, migrate.WithModel((*TableAfter)(nil)))

	// Act
	runMigrations(t, m)

	// Assert
	state := inspect(ctx)
	cmpTables(t, db.Dialect().(sqlschema.InspectorDialect), wantTables, state.Tables)
}

func testUpdatePrimaryKeys(t *testing.T, db *bun.DB) {
	// Has a composite primary key.
	type DropPKBefore struct {
		bun.BaseModel `bun:"table:drop_your_pks"`
		FirstName     string `bun:"first_name,pk"`
		LastName      string `bun:"last_name,pk"`
	}

	// This table doesn't have any primary keys at all.
	type AddNewPKBefore struct {
		bun.BaseModel `bun:"table:add_new_pk"`
		FirstName     string `bun:"first_name"`
		LastName      string `bun:"last_name"`
	}

	// Has an (identity) ID column as primary key.
	type ChangePKBefore struct {
		bun.BaseModel `bun:"table:change_pk"`
		ID            int64  `bun:"deprecated,pk,identity"`
		FirstName     string `bun:"first_name"`
		LastName      string `bun:"last_name"`
	}

	// ------------------------

	// Doesn't have any primary keys.
	type DropPKAfter struct {
		bun.BaseModel `bun:"table:drop_your_pks"`
		FirstName     string `bun:"first_name,notnull"`
		LastName      string `bun:"last_name,notnull"`
	}

	// Has a new (identity) ID column as primary key.
	type AddNewPKAfter struct {
		bun.BaseModel `bun:"table:add_new_pk"`
		ID            int64  `bun:"new_id,pk,identity"`
		FirstName     string `bun:"first_name"`
		LastName      string `bun:"last_name"`
	}

	// Has a composite primary key in place of the old ID.
	type ChangePKAfter struct {
		bun.BaseModel `bun:"table:change_pk"`
		FirstName     string `bun:"first_name,pk"`
		LastName      string `bun:"last_name,pk"`
	}

	wantTables := []sqlschema.Table{
		{
			Schema: db.Dialect().DefaultSchema(),
			Name:   "drop_your_pks",
			Columns: map[string]sqlschema.Column{
				"first_name": {
					SQLType:    sqltype.VarChar,
					IsNullable: false,
				},
				"last_name": {
					SQLType:    sqltype.VarChar,
					IsNullable: false,
				},
			},
		},
		{
			Schema: db.Dialect().DefaultSchema(),
			Name:   "add_new_pk",
			Columns: map[string]sqlschema.Column{
				"new_id": {
					SQLType:    sqltype.BigInt,
					IsNullable: false,
					IsIdentity: true,
				},
				"first_name": {
					SQLType:    sqltype.VarChar,
					IsNullable: true,
				},
				"last_name": {
					SQLType:    sqltype.VarChar,
					IsNullable: true,
				},
			},
			PK: &sqlschema.PK{Columns: sqlschema.NewComposite("new_id")},
		},
		{
			Schema: db.Dialect().DefaultSchema(),
			Name:   "change_pk",
			Columns: map[string]sqlschema.Column{
				"first_name": {
					SQLType:    sqltype.VarChar,
					IsNullable: false,
				},
				"last_name": {
					SQLType:    sqltype.VarChar,
					IsNullable: false,
				},
			},
			PK: &sqlschema.PK{Columns: sqlschema.NewComposite("first_name", "last_name")},
		},
	}

	ctx := context.Background()
	inspect := inspectDbOrSkip(t, db)
	mustResetModel(t, ctx, db,
		(*DropPKBefore)(nil),
		(*AddNewPKBefore)(nil),
		(*ChangePKBefore)(nil),
	)
	m := newAutoMigratorOrSkip(t, db, migrate.WithModel(
		(*DropPKAfter)(nil),
		(*AddNewPKAfter)(nil),
		(*ChangePKAfter)(nil)),
	)

	// Act
	runMigrations(t, m)

	// Assert
	state := inspect(ctx)
	cmpTables(t, db.Dialect().(sqlschema.InspectorDialect), wantTables, state.Tables)
}
