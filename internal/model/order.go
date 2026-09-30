package model

// Ordering. The dashboard sends the complete new order of a list; items get
// Order 0, 1, 2… in that sequence. Each list must be sent in full (every
// item exactly once), so a stale or partial view can't silently drop
// anything.

func errOrder() error { return invalid("order", "must list each item exactly once") }

// ReorderProjects sets the order of all projects.
func (d *Data) ReorderProjects(ids []string) error {
	current := make([]string, len(d.Projects))
	for i, p := range d.Projects {
		current[i] = p.ID
	}
	pos, ok := positions(ids, current, nil)
	if !ok {
		return errOrder()
	}
	for i := range d.Projects {
		d.Projects[i].Order = pos[d.Projects[i].ID]
	}
	return nil
}

// ReorderServers sets the order of a project's servers. ids must include
// every server already in the project; it may also include servers from
// other projects, which are moved into this one at that position.
func (d *Data) ReorderServers(projectID string, ids []string) error {
	if d.projectIndex(projectID) < 0 {
		return ErrNotFound
	}
	var current, movable []string
	for _, s := range d.Servers {
		if s.ProjectID == projectID {
			current = append(current, s.ID)
		} else {
			movable = append(movable, s.ID)
		}
	}
	pos, ok := positions(ids, current, movable)
	if !ok {
		return errOrder()
	}
	for i := range d.Servers {
		if p, listed := pos[d.Servers[i].ID]; listed {
			d.Servers[i].ProjectID = projectID
			d.Servers[i].Order = p
		}
	}
	return nil
}

// ReorderServices sets the order of a server's services.
func (d *Data) ReorderServices(serverID string, ids []string) error {
	if d.serverIndex(serverID) < 0 {
		return ErrNotFound
	}
	var current []string
	for _, s := range d.Services {
		if s.ServerID == serverID {
			current = append(current, s.ID)
		}
	}
	pos, ok := positions(ids, current, nil)
	if !ok {
		return errOrder()
	}
	for i := range d.Services {
		if p, listed := pos[d.Services[i].ID]; listed {
			d.Services[i].Order = p
		}
	}
	return nil
}

// positions maps each id to its index, checking that ids contains every
// member of required, no duplicates, and nothing outside required+optional.
func positions(ids, required, optional []string) (map[string]int, bool) {
	allowed := map[string]bool{}
	for _, id := range required {
		allowed[id] = true
	}
	for _, id := range optional {
		allowed[id] = true
	}
	pos := make(map[string]int, len(ids))
	for i, id := range ids {
		if !allowed[id] {
			return nil, false
		}
		if _, dup := pos[id]; dup {
			return nil, false
		}
		pos[id] = i
	}
	for _, id := range required {
		if _, ok := pos[id]; !ok {
			return nil, false
		}
	}
	return pos, true
}
