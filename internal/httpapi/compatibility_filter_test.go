package httpapi

import (
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/zukan/corvus/internal/repository"
)

func TestCallerRefIDFilteredPagination(t *testing.T) {
	pairs := readLegacyPairs(t)
	created := time.Date(2026, 10, 22, 0, 2, 0, 0, time.UTC)
	extra := repository.Comment{ID: "64b000000000000000000004", ApplicationID: pairs.Seed.ApplicationID,
		RefID: strptr(pairs.Seed.Comment.RefID), OwnerID: "user3@example.com", Content: "Another comment on the same post",
		Origin: strptr("web"), CreatedAt: created, UpdatedAt: created}
	h, token := newCompatibilityServer(t, pairs, extra)
	first := substitute(pairs.Pairs["getItem"].Response.Body["data"], map[string]string{
		"$applicationId": pairs.Seed.ApplicationID, "$commentId": pairs.Seed.CommentID,
	})
	second := object{"commentId": extra.ID, "applicationId": extra.ApplicationID, "refId": *extra.RefID,
		"ownerId": extra.OwnerID, "content": extra.Content, "origin": "web", "createdAt": "2026-10-22T00:02:00.000Z",
		"updatedAt": "2026-10-22T00:02:00.000Z", "numOfVotes": 0, "numOfUpVotes": 0, "numOfDownVotes": 0, "numOfFlags": 0, "numOfReplies": 0}
	other := object{"commentId": pairs.Seed.OtherCommentID, "applicationId": pairs.Seed.ApplicationID,
		"refId": pairs.Seed.OtherComment.RefID, "ownerId": pairs.Seed.OtherComment.OwnerID, "content": pairs.Seed.OtherComment.Content,
		"origin": pairs.Seed.OtherComment.Origin, "createdAt": pairs.Seed.OtherCreatedAt, "updatedAt": pairs.Seed.OtherCreatedAt,
		"numOfVotes": 1, "numOfUpVotes": 1, "numOfDownVotes": 0, "numOfFlags": 1, "numOfReplies": 0}
	for _, tc := range []struct {
		name, ref, sort, page string
		records               []any
		total, pages, current int
		next, prev            any
	}{
		{"ascending_first", pairs.Seed.Comment.RefID, "ASC", "1", []any{first}, 2, 2, 1, 2, nil},
		{"ascending_second", pairs.Seed.Comment.RefID, "ASC", "2", []any{second}, 2, 2, 2, nil, 1},
		{"descending_first", pairs.Seed.Comment.RefID, "DESC", "1", []any{second}, 2, 2, 1, 2, nil},
		{"descending_second", pairs.Seed.Comment.RefID, "DESC", "2", []any{first}, 2, 2, 2, nil, 1},
		{"other_post", pairs.Seed.OtherComment.RefID, "DESC", "1", []any{other}, 1, 1, 1, nil, nil},
		{"empty_post", "post-with-no-comments", "DESC", "1", []any{}, 0, 1, 1, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query := url.Values{"refId": {tc.ref}, "sort": {tc.sort}, "page": {tc.page}, "limit": {"1"}}
			res := call(h, "GET", "/v1/comments?"+query.Encode(), "", token)
			if res.Code != 200 {
				t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
			}
			want := object{"status": "success", "message": "Comments Retrieved Successfully", "data": object{
				"records": tc.records, "pageInfo": object{"currentPage": tc.current, "totalPages": tc.pages,
					"hasNext": tc.next != nil, "hasPrev": tc.prev != nil, "nextPage": tc.next, "prevPage": tc.prev,
					"pageRecordCount": len(tc.records), "totalRecord": tc.total}}}
			encoded, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			var expected, actual any
			if err := json.Unmarshal(encoded, &expected); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(res.Body.Bytes(), &actual); err != nil {
				t.Fatal(err)
			}
			if err := matchLegacyValue(expected, actual, "response"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
