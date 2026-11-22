package engine

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/ids"
)

// Storage of how things are organized: folders, labels, shared and personal
// layouts (schema 51). Who may do what is decided by the API; this is storage.

// Areas are the lists that can be organized.
var OrgAreas = []string{"pipelines", "repositories", "projects", "runs"}

func validArea(a string) bool {
	for _, x := range OrgAreas {
		if x == a {
			return true
		}
	}
	return false
}

// OrgFolder is a named group inside a scope (a level and its name).
type OrgFolder struct {
	ID          string `json:"id"`
	Scope       string `json:"scope"`
	Area        string `json:"area"`
	Parent      string `json:"parent"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Position    int    `json:"position"`
	CreatedBy   string `json:"created_by"`
	CreatedAt   int64  `json:"created_at"`
}

var ErrOrganize = errors.New("organize")

func (e *Engine) orgReady() error {
	if !e.knows(schemaOrganize) {
		return fmt.Errorf("%w: this database is older than schema %d; folders and layouts are not available", ErrOrganize, schemaOrganize)
	}
	return nil
}

// CreateFolder makes a folder. The name has no slash (a slash makes folders by name).
func (e *Engine) CreateFolder(f OrgFolder) (*OrgFolder, error) {
	if err := e.orgReady(); err != nil {
		return nil, err
	}
	f.Name = strings.TrimSpace(f.Name)
	if f.Name == "" || strings.ContainsAny(f.Name, "/\x00") || len(f.Name) > 80 {
		return nil, fmt.Errorf("%w: a folder name is 1 to 80 characters without a slash", ErrInvalid)
	}
	if !validArea(f.Area) || f.Scope == "" {
		return nil, fmt.Errorf("%w: a folder needs a scope and one of the areas %v", ErrInvalid, OrgAreas)
	}
	if f.Parent != "" {
		p, err := e.Folder(f.Parent)
		if err != nil {
			return nil, err
		}
		if p.Scope != f.Scope || p.Area != f.Area {
			return nil, fmt.Errorf("%w: the parent folder is in another scope or area", ErrInvalid)
		}
	}
	f.ID = ids.New("fld")
	f.CreatedAt = e.now().UnixMilli()
	unlock := e.writeLock()
	defer unlock()
	var dup int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM org_folders WHERE scope=? AND area=? AND parent_id=? AND name=?`, f.Scope, f.Area, f.Parent, f.Name).Scan(&dup); err != nil {
		return nil, err
	}
	if dup > 0 {
		return nil, fmt.Errorf("%w: a folder named %q is already there", ErrConflict, f.Name)
	}
	if _, err := e.db.Exec(`INSERT INTO org_folders(id,scope,area,parent_id,name,description,position,created_by,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		f.ID, f.Scope, f.Area, f.Parent, f.Name, f.Description, f.Position, f.CreatedBy, f.CreatedAt); err != nil {
		return nil, err
	}
	return &f, nil
}

// Folder returns one folder.
func (e *Engine) Folder(id string) (*OrgFolder, error) {
	if err := e.orgReady(); err != nil {
		return nil, err
	}
	f := &OrgFolder{}
	err := e.db.QueryRow(`SELECT id,scope,area,parent_id,name,description,position,created_by,created_at FROM org_folders WHERE id=?`, id).
		Scan(&f.ID, &f.Scope, &f.Area, &f.Parent, &f.Name, &f.Description, &f.Position, &f.CreatedBy, &f.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: folder %s", ErrNotFound, id)
	}
	return f, err
}

// Folders lists the folders of an area, in the scopes given (all if none), in order.
func (e *Engine) Folders(area string, scopes ...string) ([]OrgFolder, error) {
	if err := e.orgReady(); err != nil {
		return nil, err
	}
	q := `SELECT id,scope,area,parent_id,name,description,position,created_by,created_at FROM org_folders WHERE area=?`
	args := []any{area}
	if len(scopes) > 0 {
		marks := strings.TrimSuffix(strings.Repeat("?,", len(scopes)), ",")
		q += ` AND scope IN (` + marks + `)`
		for _, s := range scopes {
			args = append(args, s)
		}
	}
	rows, err := e.db.Query(q+` ORDER BY position, name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OrgFolder{}
	for rows.Next() {
		var f OrgFolder
		if err := rows.Scan(&f.ID, &f.Scope, &f.Area, &f.Parent, &f.Name, &f.Description, &f.Position, &f.CreatedBy, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// UpdateFolder renames, describes, reorders or re-parents a folder.
func (e *Engine) UpdateFolder(id string, name, description *string, position *int, parent *string) (*OrgFolder, error) {
	f, err := e.Folder(id)
	if err != nil {
		return nil, err
	}
	if name != nil {
		n := strings.TrimSpace(*name)
		if n == "" || strings.ContainsAny(n, "/\x00") || len(n) > 80 {
			return nil, fmt.Errorf("%w: a folder name is 1 to 80 characters without a slash", ErrInvalid)
		}
		f.Name = n
	}
	if description != nil {
		f.Description = *description
	}
	if position != nil {
		f.Position = *position
	}
	if parent != nil && *parent != f.Parent {
		if *parent != "" {
			all, err := e.Folders(f.Area, f.Scope)
			if err != nil {
				return nil, err
			}
			byID := map[string]OrgFolder{}
			for _, x := range all {
				byID[x.ID] = x
			}
			if _, ok := byID[*parent]; !ok {
				return nil, fmt.Errorf("%w: the parent folder is in another scope or area", ErrInvalid)
			}
			for p, n := *parent, 0; p != ""; n++ { // no folder inside itself
				if p == id || n > 64 {
					return nil, fmt.Errorf("%w: a folder cannot be moved inside itself", ErrInvalid)
				}
				p = byID[p].Parent
			}
		}
		f.Parent = *parent
	}
	unlock := e.writeLock()
	defer unlock()
	_, err = e.db.Exec(`UPDATE org_folders SET name=?, description=?, position=?, parent_id=? WHERE id=?`, f.Name, f.Description, f.Position, f.Parent, id)
	return f, err
}

// DeleteFolder removes a folder; its items and subfolders move up to its parent.
func (e *Engine) DeleteFolder(id string) error {
	f, err := e.Folder(id)
	if err != nil {
		return err
	}
	unlock := e.writeLock()
	defer unlock()
	if f.Parent == "" {
		if _, err = e.db.Exec(`DELETE FROM org_folder_items WHERE folder_id=?`, id); err != nil {
			return err
		}
	} else if _, err = e.db.Exec(`UPDATE org_folder_items SET folder_id=? WHERE folder_id=?`, f.Parent, id); err != nil {
		return err
	}
	if _, err = e.db.Exec(`UPDATE org_folders SET parent_id=? WHERE parent_id=?`, f.Parent, id); err != nil {
		return err
	}
	_, err = e.db.Exec(`DELETE FROM org_folders WHERE id=?`, id)
	return err
}

// MoveItem files an item in a folder ("" takes it out of any folder). An item lives in one folder.
func (e *Engine) MoveItem(area, item, folderID string, position int) error {
	if err := e.orgReady(); err != nil {
		return err
	}
	if !validArea(area) || item == "" {
		return fmt.Errorf("%w: an area and an item are needed", ErrInvalid)
	}
	if folderID != "" {
		f, err := e.Folder(folderID)
		if err != nil {
			return err
		}
		if f.Area != area {
			return fmt.Errorf("%w: that folder is for %s", ErrInvalid, f.Area)
		}
	}
	unlock := e.writeLock()
	defer unlock()
	if _, err := e.db.Exec(`DELETE FROM org_folder_items WHERE area=? AND item=?`, area, item); err != nil {
		return err
	}
	if folderID == "" {
		return nil
	}
	_, err := e.db.Exec(`INSERT INTO org_folder_items(area,item,folder_id,position) VALUES(?,?,?,?)`, area, item, folderID, position)
	return err
}

// FolderOf returns each item's folder in an area (items in no folder are absent).
func (e *Engine) FolderOf(area string) (map[string]string, error) {
	if err := e.orgReady(); err != nil {
		return map[string]string{}, nil
	}
	rows, err := e.db.Query(`SELECT item, folder_id FROM org_folder_items WHERE area=?`, area)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var i, f string
		if err := rows.Scan(&i, &f); err != nil {
			return nil, err
		}
		out[i] = f
	}
	return out, rows.Err()
}

// SetLabels replaces an item's labels.
func (e *Engine) SetLabels(area, item string, labels []string) error {
	if err := e.orgReady(); err != nil {
		return err
	}
	if !validArea(area) || item == "" {
		return fmt.Errorf("%w: an area and an item are needed", ErrInvalid)
	}
	clean := map[string]bool{}
	for _, l := range labels {
		l = strings.ToLower(strings.TrimSpace(l))
		if l == "" {
			continue
		}
		if len(l) > 40 || strings.ContainsAny(l, "\x00\n") {
			return fmt.Errorf("%w: a label is up to 40 characters", ErrInvalid)
		}
		clean[l] = true
	}
	if len(clean) > 20 {
		return fmt.Errorf("%w: at most 20 labels", ErrInvalid)
	}
	unlock := e.writeLock()
	defer unlock()
	if _, err := e.db.Exec(`DELETE FROM org_labels WHERE area=? AND item=?`, area, item); err != nil {
		return err
	}
	for l := range clean {
		if _, err := e.db.Exec(`INSERT INTO org_labels(area,item,label) VALUES(?,?,?)`, area, item, l); err != nil {
			return err
		}
	}
	return nil
}

// LabelsOf returns the labels of every item in an area.
func (e *Engine) LabelsOf(area string) (map[string][]string, error) {
	if !e.knows(schemaOrganize) {
		return map[string][]string{}, nil
	}
	rows, err := e.db.Query(`SELECT item, label FROM org_labels WHERE area=? ORDER BY label`, area)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var i, l string
		if err := rows.Scan(&i, &l); err != nil {
			return nil, err
		}
		out[i] = append(out[i], l)
	}
	return out, rows.Err()
}

// SharedLayout is the default arrangement of a page at a level.
type SharedLayout struct {
	Scope     string `json:"scope"`
	Page      string `json:"page"`
	Data      string `json:"data"`
	Locked    bool   `json:"locked"`
	UpdatedBy string `json:"updated_by"`
	UpdatedAt int64  `json:"updated_at"`
}

// SharedLayouts returns the shared layouts of a page, for the scopes given.
func (e *Engine) SharedLayouts(page string, scopes []string) ([]SharedLayout, error) {
	if !e.knows(schemaOrganize) || len(scopes) == 0 {
		return []SharedLayout{}, nil
	}
	args := []any{page}
	for _, s := range scopes {
		args = append(args, s)
	}
	rows, err := e.db.Query(`SELECT scope,page,data,locked,updated_by,updated_at FROM org_layouts WHERE page=? AND scope IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(scopes)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SharedLayout{}
	for rows.Next() {
		var l SharedLayout
		var locked int
		if err := rows.Scan(&l.Scope, &l.Page, &l.Data, &locked, &l.UpdatedBy, &l.UpdatedAt); err != nil {
			return nil, err
		}
		l.Locked = locked != 0
		out = append(out, l)
	}
	return out, rows.Err()
}

// SetSharedLayout stores the shared layout of a page at a scope.
func (e *Engine) SetSharedLayout(l SharedLayout) error {
	if err := e.orgReady(); err != nil {
		return err
	}
	if l.Scope == "" || l.Page == "" || len(l.Data) > 256<<10 {
		return fmt.Errorf("%w: a layout needs a scope and a page, and is up to 256 KiB", ErrInvalid)
	}
	l.UpdatedAt = e.now().UnixMilli()
	locked := 0
	if l.Locked {
		locked = 1
	}
	unlock := e.writeLock()
	defer unlock()
	if _, err := e.db.Exec(`DELETE FROM org_layouts WHERE scope=? AND page=?`, l.Scope, l.Page); err != nil {
		return err
	}
	_, err := e.db.Exec(`INSERT INTO org_layouts(scope,page,data,locked,updated_by,updated_at) VALUES(?,?,?,?,?,?)`, l.Scope, l.Page, l.Data, locked, l.UpdatedBy, l.UpdatedAt)
	return err
}

// DeleteSharedLayout removes the shared layout of a page at a scope.
func (e *Engine) DeleteSharedLayout(scope, page string) error {
	if err := e.orgReady(); err != nil {
		return err
	}
	unlock := e.writeLock()
	defer unlock()
	_, err := e.db.Exec(`DELETE FROM org_layouts WHERE scope=? AND page=?`, scope, page)
	return err
}

// UserLayout returns a person's own document for a page ("{}" if none).
func (e *Engine) UserLayout(user, page string) (string, error) {
	if !e.knows(schemaOrganize) {
		return "{}", nil
	}
	var d string
	err := e.db.QueryRow(`SELECT data FROM org_user_layouts WHERE username=? AND page=?`, user, page).Scan(&d)
	if errors.Is(err, sql.ErrNoRows) {
		return "{}", nil
	}
	return d, err
}

// SetUserLayout stores a person's own document for a page; "" or "{}" resets it.
func (e *Engine) SetUserLayout(user, page, data string) error {
	if err := e.orgReady(); err != nil {
		return err
	}
	if user == "" || page == "" || len(data) > 128<<10 {
		return fmt.Errorf("%w: a user and a page are needed, and the document is up to 128 KiB", ErrInvalid)
	}
	unlock := e.writeLock()
	defer unlock()
	if _, err := e.db.Exec(`DELETE FROM org_user_layouts WHERE username=? AND page=?`, user, page); err != nil {
		return err
	}
	if data == "" || data == "{}" {
		return nil
	}
	_, err := e.db.Exec(`INSERT INTO org_user_layouts(username,page,data,updated_at) VALUES(?,?,?,?)`, user, page, data, e.now().UnixMilli())
	return err
}

// ResetUserLayouts deletes all of a person's own layouts except the page "prefs" (favorites, dismissed).
func (e *Engine) ResetUserLayouts(user string) error {
	if err := e.orgReady(); err != nil {
		return err
	}
	unlock := e.writeLock()
	defer unlock()
	_, err := e.db.Exec(`DELETE FROM org_user_layouts WHERE username=? AND page<>'prefs'`, user)
	return err
}

// SortedFolderPaths returns each folder's path ("Payments/Api") by ID.
func FolderPaths(fs []OrgFolder) map[string]string {
	by := map[string]OrgFolder{}
	for _, f := range fs {
		by[f.ID] = f
	}
	out := map[string]string{}
	for _, f := range fs {
		var parts []string
		for c, n := f, 0; n < 64; n++ {
			parts = append([]string{c.Name}, parts...)
			p, ok := by[c.Parent]
			if !ok {
				break
			}
			c = p
		}
		out[f.ID] = strings.Join(parts, "/")
	}
	return out
}

var _ = sort.Strings
