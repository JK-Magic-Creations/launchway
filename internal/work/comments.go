package work

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path"
	"regexp"
	"strings"
)

// Comment is a comment on an item.
type Comment struct {
	ID         string `json:"id"`
	ItemRef    string `json:"item,omitempty"`
	Author     string `json:"author"`
	AuthorKind string `json:"author_kind"`
	Body       string `json:"body"`
	CreatedAt  string `json:"created_at"`
	EditedAt   string `json:"edited_at,omitempty"`
	Edits      int    `json:"edits,omitempty"`
}

var mention = regexp.MustCompile(`(^|[\s(])@([A-Za-z0-9][A-Za-z0-9._-]{0,60})`)

// Mentions finds the names mentioned with @ in a text.
func Mentions(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range mention.FindAllStringSubmatch(text, -1) {
		n := strings.TrimRight(m[2], ".-_")
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// AddComment adds a comment. Anyone who can see the item and may write in its
// scope may comment (a reporter of a confidential item always may).
func (s *Store) AddComment(ctx context.Context, c Caller, ref, body string) (*Comment, error) {
	it, err := s.Get(ctx, c, ref)
	if err != nil {
		return nil, err
	}
	if !c.canWrite(it.Scope) && it.Reporter != c.Name {
		return nil, forbidden("you may not comment on work in %s", it.Scope)
	}
	if c.Agent != nil && (it.Claim == nil || it.Claim.Agent != c.Agent.Name) {
		return nil, forbidden("an agent comments on the work it holds")
	}
	body = cleanBody(strings.TrimSpace(body))
	if body == "" || len(body) > MaxComment {
		return nil, invalid("a comment is 1 to %d bytes", MaxComment)
	}
	cm := &Comment{ID: newID("wcm"), Author: c.Name, AuthorKind: c.Kind, Body: body}
	err = s.tx(ctx, func(tx DB) error {
		now := ms(s.now())
		if _, err := tx.Exec(ctx, `INSERT INTO work_comments(id, item_id, author, author_kind, body, created_at, edited_at) VALUES(?,?,?,?,?,?,0)`, cm.ID, it.ID, c.Name, c.Kind, body, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE work_items SET updated_at = ? WHERE id = ?`, now, it.ID); err != nil {
			return err
		}
		if err := s.event(ctx, tx, it.ID, c.Actor, "commented", 0, map[string]any{"comment": cm.ID}); err != nil {
			return err
		}
		cm.CreatedAt = iso(now)
		return s.notifyComment(ctx, tx, c, it, body)
	})
	if err != nil {
		return nil, err
	}
	cm.ItemRef = it.Ref
	return cm, nil
}

// notifyComment tells the mentioned and, for a comment, the watchers.
func (s *Store) notifyComment(ctx context.Context, tx DB, c Caller, it *Item, body string) error {
	done := map[string]bool{}
	for _, n := range Mentions(body) {
		// Someone who cannot see a confidential item is not told of it.
		if it.Confidential && n != it.Reporter && !slicesHasAssignee(it, n) {
			continue
		}
		done[n] = true
		if err := s.notify(ctx, tx, n, c.Name, InboxMentioned, it.ID, c.Name+" mentioned you on "+it.Ref+": "+it.Title); err != nil {
			return err
		}
	}
	ws, err := queryAll(ctx, tx, scanString, `SELECT who FROM work_watchers WHERE item_id = ?`, it.ID)
	if err != nil {
		return err
	}
	for _, w := range ws {
		if !done[w] {
			if err := s.notify(ctx, tx, w, c.Name, InboxComment, it.ID, c.Name+" commented on "+it.Ref); err != nil {
				return err
			}
		}
	}
	return nil
}

func slicesHasAssignee(it *Item, n string) bool {
	for _, a := range it.Assignees {
		if a.Name == n {
			return true
		}
	}
	return false
}

// EditComment changes a comment its author wrote; the earlier text is kept.
func (s *Store) EditComment(ctx context.Context, c Caller, id, body string) (*Comment, error) {
	var itemID, author, old string
	err := s.db.QueryRow(ctx, `SELECT item_id, author, body FROM work_comments WHERE id = ?`, id).Scan(&itemID, &author, &old)
	if isNoRows(err) {
		return nil, notFound("no comment " + id)
	}
	if err != nil {
		return nil, err
	}
	it, err := s.load(ctx, s.db, itemID)
	if err != nil || !c.canSee(it) {
		return nil, notFound("no comment " + id)
	}
	if author != c.Name || c.Kind == KindSystem {
		return nil, forbidden("only the author edits a comment")
	}
	body = cleanBody(strings.TrimSpace(body))
	if body == "" || len(body) > MaxComment {
		return nil, invalid("a comment is 1 to %d bytes", MaxComment)
	}
	if body == old {
		return s.commentByID(ctx, id)
	}
	err = s.tx(ctx, func(tx DB) error {
		var rev int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(rev), 0) + 1 FROM work_comment_edits WHERE comment_id = ?`, id).Scan(&rev); err != nil {
			return err
		}
		now := ms(s.now())
		if _, err := tx.Exec(ctx, `INSERT INTO work_comment_edits(comment_id, rev, body, edited_by, at) VALUES(?,?,?,?,?)`, id, rev, old, c.Name, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE work_comments SET body = ?, edited_at = ? WHERE id = ?`, body, now, id); err != nil {
			return err
		}
		return s.event(ctx, tx, itemID, c.Actor, "comment_edited", 0, map[string]any{"comment": id})
	})
	if err != nil {
		return nil, err
	}
	return s.commentByID(ctx, id)
}

func (s *Store) commentByID(ctx context.Context, id string) (*Comment, error) {
	cm := &Comment{}
	var cr, ed int64
	err := s.db.QueryRow(ctx, `SELECT id, author, author_kind, body, created_at, edited_at, (SELECT COUNT(*) FROM work_comment_edits e WHERE e.comment_id = work_comments.id) FROM work_comments WHERE id = ?`, id).
		Scan(&cm.ID, &cm.Author, &cm.AuthorKind, &cm.Body, &cr, &ed, &cm.Edits)
	cm.CreatedAt, cm.EditedAt = iso(cr), iso(ed)
	return cm, err
}

// Comments lists an item's comments, oldest first.
func (s *Store) Comments(ctx context.Context, c Caller, ref string) ([]Comment, error) {
	it, err := s.Get(ctx, c, ref)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT id, author, author_kind, body, created_at, edited_at, (SELECT COUNT(*) FROM work_comment_edits e WHERE e.comment_id = work_comments.id)
		FROM work_comments WHERE item_id = ? ORDER BY created_at, id`, it.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Comment{}
	for rows.Next() {
		var cm Comment
		var cr, ed int64
		if err := rows.Scan(&cm.ID, &cm.Author, &cm.AuthorKind, &cm.Body, &cr, &ed, &cm.Edits); err != nil {
			return nil, err
		}
		cm.CreatedAt, cm.EditedAt, cm.ItemRef = iso(cr), iso(ed), it.Ref
		out = append(out, cm)
	}
	return out, rows.Err()
}

// CommentEdits lists the earlier texts of a comment, oldest first.
func (s *Store) CommentEdits(ctx context.Context, c Caller, id string) ([]Comment, error) {
	var itemID string
	if err := s.db.QueryRow(ctx, `SELECT item_id FROM work_comments WHERE id = ?`, id).Scan(&itemID); err != nil {
		return nil, notFound("no comment " + id)
	}
	if _, err := s.Get(ctx, c, itemID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT rev, body, edited_by, at FROM work_comment_edits WHERE comment_id = ? ORDER BY rev`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Comment{}
	for rows.Next() {
		var cm Comment
		var rev int
		var at int64
		if err := rows.Scan(&rev, &cm.Body, &cm.Author, &at); err != nil {
			return nil, err
		}
		cm.ID, cm.CreatedAt, cm.Edits = id, iso(at), rev
		out = append(out, cm)
	}
	return out, rows.Err()
}

// Attachment is a file on an item. A transcript or summary an agent posted is
// an attachment of kind "transcript".
type Attachment struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	Kind        string `json:"kind"`
	By          string `json:"by"`
	At          string `json:"at"`
}

// MaxAttachment is the largest file on an item.
const MaxAttachment = 10 << 20

// MaxAttachmentsPerItem bounds what one item holds.
const MaxAttachmentsPerItem = 50

// AddAttachment stores a file on an item.
func (s *Store) AddAttachment(ctx context.Context, c Caller, ref, name, contentType, kind string, data []byte) (*Attachment, error) {
	it, err := s.Get(ctx, c, ref)
	if err != nil {
		return nil, err
	}
	if !c.canWrite(it.Scope) {
		return nil, forbidden("you may not change work in %s", it.Scope)
	}
	if c.Agent != nil && (it.Claim == nil || it.Claim.Agent != c.Agent.Name) {
		return nil, forbidden("an agent attaches files to the work it holds")
	}
	name = path.Base(strings.ReplaceAll(cleanTitle(name), `\`, "/"))
	if name == "" || name == "." || name == "/" || len(name) > 200 {
		return nil, invalid("a file needs a name of up to 200 characters")
	}
	if len(data) == 0 || len(data) > MaxAttachment {
		return nil, invalid("a file is 1 byte to %d MB", MaxAttachment>>20)
	}
	if kind == "" {
		kind = "file"
	}
	if kind != "file" && kind != "transcript" && kind != "evidence" {
		return nil, invalid("an attachment is a file, a transcript or evidence")
	}
	if contentType == "" || len(contentType) > 100 {
		contentType = "application/octet-stream"
	}
	sum := sha256.Sum256(data)
	a := &Attachment{ID: newID("wat"), Name: name, ContentType: contentType, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Kind: kind, By: c.Name}
	err = s.tx(ctx, func(tx DB) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM work_attachments WHERE item_id = ?`, it.ID).Scan(&n); err != nil {
			return err
		}
		if n >= MaxAttachmentsPerItem {
			return conflict("an item holds at most %d files", MaxAttachmentsPerItem)
		}
		now := ms(s.now())
		a.At = iso(now)
		if _, err := tx.Exec(ctx, `INSERT INTO work_attachments(id, item_id, name, content_type, size, sha256, kind, added_by, at, data) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			a.ID, it.ID, name, contentType, a.Size, a.SHA256, kind, c.Name, now, data); err != nil {
			return err
		}
		return s.event(ctx, tx, it.ID, c.Actor, "attached", 0, map[string]any{"name": name, "kind": kind, "size": a.Size})
	})
	if err != nil {
		return nil, err
	}
	return a, nil
}

// Attachments lists an item's files.
func (s *Store) Attachments(ctx context.Context, c Caller, ref string) ([]Attachment, error) {
	it, err := s.Get(ctx, c, ref)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT id, name, content_type, size, sha256, kind, added_by, at FROM work_attachments WHERE item_id = ? ORDER BY at`, it.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Attachment{}
	for rows.Next() {
		var a Attachment
		var at int64
		if err := rows.Scan(&a.ID, &a.Name, &a.ContentType, &a.Size, &a.SHA256, &a.Kind, &a.By, &at); err != nil {
			return nil, err
		}
		a.At = iso(at)
		out = append(out, a)
	}
	return out, rows.Err()
}

// Attachment reads one file with its bytes.
func (s *Store) AttachmentData(ctx context.Context, c Caller, id string) (*Attachment, []byte, error) {
	var a Attachment
	var itemID string
	var at int64
	var data []byte
	err := s.db.QueryRow(ctx, `SELECT id, item_id, name, content_type, size, sha256, kind, added_by, at, data FROM work_attachments WHERE id = ?`, id).
		Scan(&a.ID, &itemID, &a.Name, &a.ContentType, &a.Size, &a.SHA256, &a.Kind, &a.By, &at, &data)
	if isNoRows(err) {
		return nil, nil, notFound("no file " + id)
	}
	if err != nil {
		return nil, nil, err
	}
	if _, err := s.Get(ctx, c, itemID); err != nil {
		return nil, nil, notFound("no file " + id)
	}
	a.At = iso(at)
	return &a, data, nil
}
