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

	// Collect all items across pages.
	var allItems []UploadRecord
	cursor := ""

	for page := 0; page < 10; page++ {
		list, err := ListUploads(from, to, "", limit, cursor)
		require.NoError(t, err, "page %d should not error", page)
		require.NotNil(t, list.Items, "page %d items must not be nil", page)
		require.LessOrEqual(t, len(list.Items), limit,
			"page %d should return at most %d items", page, limit)

		allItems = append(allItems, list.Items...)

		if len(list.Items) < limit {
			// Last (or only) page — cursor must be empty.
			require.Empty(t, list.NextCursor,
				"page %d: expected empty cursor on final page", page)
			break
		}

		require.NotEmpty(t, list.NextCursor,
			"page %d: expected cursor when more items exist", page)
		cursor = list.NextCursor
	}

	// Verify we collected all expected items.
	require.Len(t, allItems, count,
		"should collect exactly %d items across all pages", count)

	// Verify no duplicates across pages.
	seen := make(map[string]bool)
	for _, item := range allItems {
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

// TestListing_Pagination_LimitOne creates three uploads and paginates
// with limit=1, verifying that each page returns a single distinct
// record. When limit == 1 and there are N items, every page has
// exactly 1 item == limit, so a cursor is always returned except on
// the trailing empty page.
func TestListing_Pagination_LimitOne(t *testing.T) {
	const count = 3
	from, to := paginationFromTo(paginationDateLimitOne)

	ids := make([]string, count)
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
		ids[i] = cr.ID
	}

	// Page 1.
	list1, err := ListUploads(from, to, "", 1, "")
	require.NoError(t, err)
	require.Len(t, list1.Items, 1, "page 1 should have exactly 1 item")
	require.NotEmpty(t, list1.NextCursor, "page 1 should have a cursor")

	// Page 2.
	list2, err := ListUploads(from, to, "", 1, list1.NextCursor)
	require.NoError(t, err)
	require.Len(t, list2.Items, 1, "page 2 should have exactly 1 item")
	require.NotEmpty(t, list2.NextCursor, "page 2 should have a cursor")

	// Page 3 — last data page: limit was reached so cursor is non-empty.
	list3, err := ListUploads(from, to, "", 1, list2.NextCursor)
	require.NoError(t, err)
	require.Len(t, list3.Items, 1, "page 3 should have exactly 1 item")
	require.NotEmpty(t, list3.NextCursor,
		"page 3 has limit=1 items, so cursor is non-empty (trail to next page)")

	// Page 4 — trailing empty page, cursor must be empty.
	list4, err := ListUploads(from, to, "", 1, list3.NextCursor)
	require.NoError(t, err)
	require.Empty(t, list4.Items, "page 4 should be empty (no more records)")
	require.Empty(t, list4.NextCursor, "page 4 should have empty cursor")

	// All data pages must return different records.
	require.NotEqual(t, list1.Items[0].ID, list2.Items[0].ID,
		"page 1 and page 2 must return different records")
	require.NotEqual(t, list2.Items[0].ID, list3.Items[0].ID,
		"page 2 and page 3 must return different records")
	require.NotEqual(t, list1.Items[0].ID, list3.Items[0].ID,
		"page 1 and page 3 must return different records")
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

	for i := 0; i < count; i++ {
		localID := MakeLocalIdentifier(t, fmt.Sprintf("default-limit-%d", i))
		body := CreateUploadBody{
			LocalIdentifier: localID,
			Filename:        fmt.Sprintf("DFL_%04d.jpg", i),
			CreationDate:    paginationDateDefault,
		}
		_, status, err := CreateUpload(body)
		require.NoError(t, err, "create upload %d should not error", i)
		require.Equal(t, http.StatusCreated, status, "create upload %d should return 201", i)
	}

	list, err := ListUploads(from, to, "", 0, "")
	require.NoError(t, err)
	require.Len(t, list.Items, count,
		"default limit should return all %d matching items", count)
	require.Empty(t, list.NextCursor,
		"cursor should be empty when all results fit within default limit")
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
