package store

import (
	"context"
	"regexp"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestAccessRoutesOrdersDistinctSelectedExpression(t *testing.T) {
	store, mock := mockStore(t)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT DISTINCT b.controller_id::text FROM relay_bindings b JOIN relay_controllers c ON c.controller_id=b.controller_id WHERE b.installation_id=$1 AND ($2::bigint=0 OR b.repository_id=$2) AND b.revoked_at IS NULL AND c.state='active' ORDER BY b.controller_id::text LIMIT 1001")).
		WithArgs(int64(10), int64(20)).
		WillReturnRows(pgxmock.NewRows([]string{"controller_id"}).AddRow(testController))

	routes, err := store.AccessRoutes(context.Background(), 10, 20)
	if err != nil {
		t.Fatalf("AccessRoutes() error = %v", err)
	}
	if len(routes) != 1 || routes[0] != testController {
		t.Fatalf("AccessRoutes() = %v, want [%s]", routes, testController)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("mock expectations: %v", err)
	}
}
