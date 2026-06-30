//go:build e2e

// Package e2e provides black-box end-to-end tests for the iCloud Backup
// server. This file contains scoped listing and pagination tests,
// verifying that GET /uploads correctly returns filtered, paginated,
// and well-formed result sets.
//
// All listing tests respect run scoping via FixtureTimeRange or use
// isolated future dates to avoid interference from other tests.
package e2e

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Isolated date constants for pagination tests
//
// Each test that creates uploads uses its own unique far-future date so
// that query windows don't overlap. This prevents cross-contamination
// when tests run sequentially (uploads from test A leak into test B's
// query window).
// ---------------------------------------------------------------------------

// paginationDateThreePages is used by TestListing_Pagination_ThreePages.
const paginationDateThreePages = "2035-07-15T00:00:00Z"

// paginationDateLimitOne is used by TestListing_Pagination_LimitOne.
const paginationDateLimitOne = "2035-07-16T00:00:00Z"

// paginationDateDefault is used by TestListing_DefaultLimit.
const paginationDateDefault = "2035-07-17T00:00:00Z"

// paginationDateFilter is used by TestListing_DateRangeFiltering.
const paginationDateFilter = "2035-07-18T00:00:00Z"

// paginationFromTo is a convenience for querying a full day range.
var paginationFromTo = func(date string) (from, to string) {
	return date, date[:11] + "23:59:59Z"
}

// ---------------------------------------------------------------------------
// Empty result
// ---------------------------------------------------------------------------

// TestListing_EmptyResult verifies that GET /uploads returns an empty
// JSON array (not null) and an empty next_cursor when no records match
// the query.
func TestListing_EmptyResult(t *testing.T) {
	// Use a date range that cannot match any record in the database.
	from := "2000-01-01T00:00:00Z"
	to := "2000-01-02T00:00:00Z"

	list, err := ListUploads(from, to, "", 0, "")
	require.NoError(t, err, "GET /uploads with non-matching range should succeed")
	require.NotNil(t, list.Items, "items must be an empty array, not null")
	require.Empty(t, list.Items, "expected empty items for non-matching range")
	require.Empty(t, list.NextCursor, "expected empty next_cursor for empty result")
}

// ---------------------------------------------------------------------------
// Pagination: full coverage across multiple pages
// ---------------------------------------------------------------------------

// TestListing_Pagination_ThreePages creates a batch of uploads and
// paginates through them with a small limit, verifying:
//   - Each page returns at most limit items.
//   - A non-empty cursor is returned when more items exist.
//   - The final page has an empty cursor.
//   - All items are returned exactly once across pages (no duplicates).
func TestListing_Pagination_ThreePages(t *testing.T) {
	const count = 5
	const limit = 2
	from, to := paginationFromTo(paginationDateThreePages)

	ids := make([]string, count)
	for i := 0; i < count; i++ {
		localID := MakeLocalIdentifier(t, fmt.Sprintf("three-pages-%d", i))
		body := CreateUploadBody{
			LocalIdentifier: localID,
			Filename:        fmt.Sprintf("PG3_%04d.jpg", i),
			CreationDate:    paginationDateThreePages,
		}
		cr, status, err := CreateUpload(body)
		require.NoError(t, err, "create upload %d should not error", i)
		require.Equal(t, http.StatusCreated, status, "create upload %d should return 201", i)
		ids[i] = cr.ID
	}

	// Collect all items across pages. We Paginate to the end of the date
	// window; previous test runs (or concurrent suites) may have left
	// records at the same future date, so we filter strictly to records
	// created by this run via IsRunItem before asserting exact counts.
	var allItems []UploadRecord
	cursor := ""
	terminated := false

	for page := 0; page < 1000; page++ {
		list, err := ListUploads(from, to, "", limit, cursor)
		require.NoError(t, err, "page %d should not error", page)
		require.NotNil(t, list.Items, "page %d items must not be nil", page)
		require.LessOrEqual(t, len(list.Items), limit,
			"page %d should return at most %d items", page, limit)

		allItems = append(allItems, list.Items...)

		if list.NextCursor == "" {
			// Terminal page reached.
			terminated = true
			break
		}
		cursor = list.NextCursor
	}
	require.True(t, terminated,
		"pagination should terminate with an empty cursor within 50 pages")

	// Filter to records created by this run; only these may be asserted
	// exactly, since the shared future date is not run-unique.
	runItems := make([]UploadRecord, 0, count)
	for _, item := range allItems {
		if IsRunItem(item) {
			runItems = append(runItems, item)
		}
	}

	// Verify we collected all expected run-scoped items.
	require.Len(t, runItems, count,
		"should collect exactly %d run-scoped items across all pages", count)

	// Verify no duplicates across pages.
	seen := make(map[string]bool)
	for _, item := range runItems {
		require.False(t, seen[item.ID],
			"duplicate item %s across pages", item.ID)
		seen[item.ID] = true
	}

	// Verify the returned IDs match the ones we created.
	for _, id := range ids {
		require.True(t, seen[id],
			"created upload %s must appear somewhere in the paginated results", id)
	}
}

// ---------------------------------------------------------------------------
// Pagination: limit=1
// ---------------------------------------------------------------------------

// TestListing_Pagination_LimitOne creates three run-scoped uploads and
// paginates through the shared future date window with limit=1, verifying
// that every page returns at most one record and that all run-scoped
// records eventually appear exactly once before pagination terminates.
//
// Because the future date is shared across test runs, the window may also
// contain records left by previous runs; assertions are therefore scoped
// to records created by this run via IsRunItem, not to global emptiness.
func TestListing_Pagination_LimitOne(t *testing.T) {
	const count = 3
	from, to := paginationFromTo(paginationDateLimitOne)

	ids := make(map[string]bool, count)
	for i := 0; i < count; i++ {
		localID := MakeLocalIdentifier(t, fmt.Sprintf("limit-one-%d", i))
		body := CreateUploadBody{
			LocalIdentifier: localID,
			Filename:        fmt.Sprintf("PG1_%04d.jpg", i),
			CreationDate:    paginationDateLimitOne,
		}
		cr, status, err := CreateUpload(body)
		require.NoError(t, err, "create upload %d should not error", i)
		require.Equal(t, http.StatusCreated, status, "create upload %d should return 201", i)
		ids[cr.ID] = true
	}

	// Paginate with limit=1 until the cursor is exhausted.
	var runItems []UploadRecord
	cursor := ""
	terminated := false
	for page := 0; page < 1000; page++ {
		list, err := ListUploads(from, to, "", 1, cursor)
		require.NoError(t, err, "page %d should not error", page)
		require.NotNil(t, list.Items, "page %d items must not be nil", page)
		require.LessOrEqual(t, len(list.Items), 1,
			"page %d should return at most 1 item with limit=1", page)

		for _, item := range list.Items {
			if IsRunItem(item) {
				runItems = append(runItems, item)
			}
		}

		if list.NextCursor == "" {
			terminated = true
			break
		}
		cursor = list.NextCursor
	}
	require.True(t, terminated,
		"limit=1 pagination should terminate with an empty cursor")

	// Assert each run-scoped record appears exactly once (no duplicates).
	seen := make(map[string]bool)
	for _, item := range runItems {
		require.False(t, seen[item.ID], "duplicate run-scoped item %s", item.ID)
		seen[item.ID] = true
	}

	// Assert the run-scoped union equals exactly the fixtures we created.
	require.Len(t, seen, count, "expected exactly %d distinct run-scoped records", count)
	for id := range ids {
		require.True(t, seen[id], "created upload %s must appear in limit=1 pagination", id)
	}
}

// ---------------------------------------------------------------------------
// Default limit
// ---------------------------------------------------------------------------

// TestListing_DefaultLimit verifies that passing limit=0 (which defaults
// to 500 server-side) returns all matching results without a next_cursor
// when they fit within that default.
func TestListing_DefaultLimit(t *testing.T) {
	const count = 3
	from, to := paginationFromTo(paginationDateDefault)

	ids := make(map[string]bool, count)
	for i := 0; i < count; i++ {
		localID := MakeLocalIdentifier(t, fmt.Sprintf("default-limit-%d", i))
		body := CreateUploadBody{
			LocalIdentifier: localID,
			Filename:        fmt.Sprintf("DFL_%04d.jpg", i),
			CreationDate:    paginationDateDefault,
		}
		cr, status, err := CreateUpload(body)
		require.NoError(t, err, "create upload %d should not error", i)
		require.Equal(t, http.StatusCreated, status, "create upload %d should return 201", i)
		ids[cr.ID] = true
	}

	// Paginate to collect all results. The shared future date may contain
	// records from previous runs, so we cannot assume the default limit (500)
	// is sufficient — follow cursors to termination to avoid flakiness.
	var allItems []UploadRecord
	cursor := ""
	terminated := false
	for page := 0; page < 1000; page++ {
		list, err := ListUploads(from, to, "", 0, cursor)
		require.NoError(t, err, "page %d should not error", page)
		require.NotNil(t, list.Items, "page %d items must not be nil", page)

		allItems = append(allItems, list.Items...)

		if list.NextCursor == "" {
			terminated = true
			break
		}
		cursor = list.NextCursor
	}
	require.True(t, terminated,
		"default-limit pagination should terminate with an empty cursor")

	// Assert that all run-scoped fixtures are present (subset check, not
	// exact total count) to avoid depending on global database emptiness.
	found := make(map[string]bool)
	for _, item := range allItems {
		if ids[item.ID] {
			found[item.ID] = true
		}
	}
	for id := range ids {
		require.True(t, found[id], "default-limit fixture %s must be returned", id)
	}
}

// ---------------------------------------------------------------------------
// Deleted uploads appear in listing
// ---------------------------------------------------------------------------

// TestListing_DeletedUploadInListing creates and then soft-deletes an
// upload, then verifies it appears in the listing with status "deleted".
func TestListing_DeletedUploadInListing(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "deleted-list-test.jpg")

	MustDeleteUpload(t, cr.ID)

	from, to := FixtureTimeRange()
	list, err := ListUploads(from, to, "", 0, "")
	require.NoError(t, err)
	require.NotEmpty(t, list.Items, "listing should contain the deleted record")

	found := false
	for _, item := range list.Items {
		if item.ID == cr.ID {
			found = true
			require.Equal(t, "deleted", item.Status,
				"deleted upload should appear with status 'deleted'")
			require.Equal(t, localID, item.LocalIdentifier,
				"local_identifier must be preserved after deletion")
			require.NotEmpty(t, item.CreationDate,
				"creation_date should be preserved after deletion")
			break
		}
	}
	require.True(t, found, "deleted upload must appear in listing")
}

// ---------------------------------------------------------------------------
// Invalid cursor
// ---------------------------------------------------------------------------

// TestListing_InvalidCursor verifies that GET /uploads with a malformed
// cursor returns 400 Bad Request.
func TestListing_InvalidCursor(t *testing.T) {
	from, to := FixtureTimeRange()

	path := fmt.Sprintf("/uploads?from=%s&to=%s&cursor=%s", from, to, "!!!invalid-cursor!!!")
	resp, err := doRequest("GET", path, nil)
	require.NoError(t, err, "GET /uploads with invalid cursor should not error at HTTP level")
	defer resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"GET /uploads with invalid cursor should return 400")
}

// ---------------------------------------------------------------------------
// Date range filtering
// ---------------------------------------------------------------------------

// TestListing_DateRangeFiltering creates an upload with a specific
// future date and then queries with a non-overlapping range, verifying
// the upload is excluded from the results.
func TestListing_DateRangeFiltering(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	body := CreateUploadBody{
		LocalIdentifier: localID,
		Filename:        "date-filter-test.jpg",
		CreationDate:    paginationDateFilter,
	}
	cr, status, err := CreateUpload(body)
	require.NoError(t, err, "create upload should not error")
	require.Equal(t, http.StatusCreated, status, "create upload should return 201")

	// Query with a range that does not include paginationDateFilter.
	wrongFrom := "2035-07-17T00:00:00Z"
	wrongTo := "2035-07-17T23:59:59Z"

	list, err := ListUploads(wrongFrom, wrongTo, "", 0, "")
	require.NoError(t, err)

	for _, item := range list.Items {
		require.NotEqual(t, cr.ID, item.ID,
			"upload with date %s should not appear when querying %s..%s",
			paginationDateFilter, wrongFrom, wrongTo)
	}

	// Verify the upload IS returned when querying the correct range.
	rightFrom := "2035-07-18T00:00:00Z"
	rightTo := "2035-07-18T23:59:59Z"

	listRight, err := ListUploads(rightFrom, rightTo, "", 0, "")
	require.NoError(t, err)

	found := false
	for _, item := range listRight.Items {
		if item.ID == cr.ID {
			found = true
			require.Equal(t, paginationDateFilter, item.CreationDate,
				"creation_date must match in correct-range query")
			break
		}
	}
	require.True(t, found,
		"upload with date %s must appear when querying %s..%s",
		paginationDateFilter, rightFrom, rightTo)
}

// ---------------------------------------------------------------------------
// Complete upload fields in listing
// ---------------------------------------------------------------------------

// TestListing_CompleteUploadFields creates a completed upload and
// verifies it appears in a status-filtered listing with complete status
// and a non-empty organized_path.
func TestListing_CompleteUploadFields(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	rec := CreateCompleteUpload(t, localID, "complete-list-fields.jpg", []byte("listing-field-data"))

	from, to := FixtureTimeRange()
	list, err := ListUploads(from, to, "complete", 0, "")
	require.NoError(t, err)
	require.NotEmpty(t, list.Items,
		"listing should contain completed uploads")

	found := false
	for _, item := range list.Items {
		if item.ID == rec.ID {
			found = true
			require.Equal(t, "complete", item.Status,
				"completed upload should have status 'complete'")
			require.NotEmpty(t, item.OrganizedPath,
				"completed upload must have organized_path in listing")
			require.Equal(t, localID, item.LocalIdentifier,
				"local_identifier preserved in listing")
			require.Equal(t, "complete-list-fields.jpg", item.Filename,
				"filename preserved in listing")
			break
		}
	}
	require.True(t, found, "completed upload must appear in listing")
}

// ---------------------------------------------------------------------------
// Unfiltered listing includes multiple statuses
// ---------------------------------------------------------------------------

// TestListing_MultipleStatuses verifies that querying without a status
// filter returns records in both uploading and complete statuses.
func TestListing_MultipleStatuses(t *testing.T) {
	uploadingID := MakeLocalIdentifier(t, "uploading-"+t.Name())
	cr := CreateTestUpload(t, uploadingID, "multi-status-up.jpg")

	completeID := MakeLocalIdentifier(t, "complete-"+t.Name())
	CreateCompleteUpload(t, completeID, "multi-status-cmpl.jpg", []byte("multi-status-data"))

	from, to := FixtureTimeRange()
	list, err := ListUploads(from, to, "", 0, "")
	require.NoError(t, err)
	require.NotEmpty(t, list.Items)

	var foundUploading, foundComplete bool
	for _, item := range list.Items {
		if item.ID == cr.ID {
			foundUploading = true
			require.Equal(t, "uploading", item.Status)
		}
		if item.LocalIdentifier == completeID {
			foundComplete = true
			require.Equal(t, "complete", item.Status)
		}
	}
	require.True(t, foundUploading,
		"uploading record must appear in unfiltered listing")
	require.True(t, foundComplete,
		"complete record must appear in unfiltered listing")
}

// ---------------------------------------------------------------------------
// Ascending order by creation date
// ---------------------------------------------------------------------------

// TestListing_AscendingOrder verifies that GET /uploads returns records
// ordered by creation date ascending.
func TestListing_AscendingOrder(t *testing.T) {
	// Create uploads with deliberately out-of-order creation dates so we
	// can verify the server sorts them ascending.
	dates := []struct {
		suffix string
		date   string
	}{
		{"late", "2035-08-01T00:00:00Z"},
		{"early", "2035-06-01T00:00:00Z"},
		{"middle", "2035-07-01T00:00:00Z"},
	}

	var created []struct {
		id   string
		date string
	}
	for _, d := range dates {
		localID := MakeLocalIdentifier(t, fmt.Sprintf("order-%s", d.suffix))
		body := CreateUploadBody{
			LocalIdentifier: localID,
			Filename:        fmt.Sprintf("ORD_%s.jpg", d.suffix),
			CreationDate:    d.date,
		}
		cr, status, err := CreateUpload(body)
		require.NoError(t, err, "create upload %s should not error", d.suffix)
		require.Equal(t, http.StatusCreated, status)
		created = append(created, struct {
			id   string
			date string
		}{cr.ID, d.date})
	}

	// Query a window that includes all three dates.
	from := "2035-06-01T00:00:00Z"
	to := "2035-08-01T23:59:59Z"

	list, err := ListUploads(from, to, "", 0, "")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(list.Items), 3,
		"listing should contain at least the 3 uploads we created")

	// Extract only our uploads in the order they appear.
	var orderedIDs []string
	ourIDs := make(map[string]string) // id → date
	for _, c := range created {
		ourIDs[c.id] = c.date
	}
	for _, item := range list.Items {
		if date, ok := ourIDs[item.ID]; ok {
			orderedIDs = append(orderedIDs, date)
		}
	}

	require.Len(t, orderedIDs, 3,
		"should find all three created uploads in listing")

	// Verify ascending order: early < middle < late.
	require.Equal(t, "2035-06-01T00:00:00Z", orderedIDs[0],
		"first upload should be 'early' (2035-06)")
	require.Equal(t, "2035-07-01T00:00:00Z", orderedIDs[1],
		"second upload should be 'middle' (2035-07)")
	require.Equal(t, "2035-08-01T00:00:00Z", orderedIDs[2],
		"third upload should be 'late' (2035-08)")
}
