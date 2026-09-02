package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func pageRequest(query string) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/admin/projects"+query, nil)
}

func TestPaginate(t *testing.T) {
	t.Parallel()

	rows := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l"}

	tests := []struct {
		name      string
		query     string
		wantItems []string
		wantPages int
		wantSize  int
	}{
		{
			name:      "defaults to ten per page",
			query:     "",
			wantItems: rows[:10],
			wantPages: 2,
			wantSize:  10,
		},
		{
			name:      "second page is the remainder",
			query:     "?per=10&page=2",
			wantItems: rows[10:],
			wantPages: 2,
			wantSize:  10,
		},
		{
			name:      "five per page",
			query:     "?per=5&page=3",
			wantItems: rows[10:],
			wantPages: 3,
			wantSize:  5,
		},
		{
			name:      "all is one page of everything",
			query:     "?per=all",
			wantItems: rows,
			wantPages: 1,
			wantSize:  0,
		},
		{
			// A bookmarked ?page=9 after rows were deleted should land on the
			// end of the list, not on a blank table.
			name:      "a page past the end clamps to the last one",
			query:     "?per=10&page=9",
			wantItems: rows[10:],
			wantPages: 2,
			wantSize:  10,
		},
		{
			name:      "an unoffered size falls back to the default",
			query:     "?per=7",
			wantItems: rows[:10],
			wantPages: 2,
			wantSize:  10,
		},
		{
			name:      "nonsense is not an error",
			query:     "?per=banana&page=-3",
			wantItems: rows[:10],
			wantPages: 2,
			wantSize:  10,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			page := paginate(rows, pageRequest(test.query))

			if strings.Join(page.Items, "") != strings.Join(test.wantItems, "") {
				t.Fatalf("items = %v, want %v", page.Items, test.wantItems)
			}
			if page.Pages != test.wantPages {
				t.Fatalf("pages = %d, want %d", page.Pages, test.wantPages)
			}
			if page.Size != test.wantSize {
				t.Fatalf("size = %d, want %d", page.Size, test.wantSize)
			}
			if page.Total != len(rows) {
				t.Fatalf("total = %d, want %d - the count is of everything, not of the page", page.Total, len(rows))
			}
		})
	}
}

func TestPaginateEmptyList(t *testing.T) {
	t.Parallel()
	page := paginate([]string{}, pageRequest(""))
	if len(page.Items) != 0 || page.Pages != 1 || page.Number != 1 {
		t.Fatalf("empty list paginated to %+v", page)
	}
}

// Changing page must not drop whatever filter the screen already had on it.
func TestPageQueryKeepsOtherParameters(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodGet, "/admin/workspaces?project=platform&per=5&page=2", nil)

	got := pageQuery(request, "/admin/workspaces", 5, 3)
	if !strings.Contains(got, "project=platform") {
		t.Fatalf("the project filter was dropped: %s", got)
	}
	if !strings.Contains(got, "page=3") || !strings.Contains(got, "per=5") {
		t.Fatalf("page or size missing: %s", got)
	}

	// Page one is the default, so it is left out rather than spelled.
	if first := pageQuery(request, "/admin/workspaces", 5, 1); strings.Contains(first, "page=") {
		t.Fatalf("page 1 should not be in the query: %s", first)
	}
}
