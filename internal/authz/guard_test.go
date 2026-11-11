package authz

import (
	"testing"

	"voice-timeline-renderer/internal/domain"
)

type fakeStore struct {
	projects    map[string]domain.Project
	memberships map[string]domain.Membership
	grants      map[string]domain.Grant
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		projects:    map[string]domain.Project{},
		memberships: map[string]domain.Membership{},
		grants:      map[string]domain.Grant{},
	}
}

func (store *fakeStore) key(projectID, actorID string) string { return projectID + "/" + actorID }

func (store *fakeStore) GetProject(projectID string) (domain.Project, error) {
	if project, ok := store.projects[projectID]; ok {
		return project, nil
	}
	return domain.Project{}, domain.E(domain.KindNotFound, "project_not_found", "project_id", "project does not exist")
}

func (store *fakeStore) GetMembership(projectID, actorID string) (domain.Membership, error) {
	if membership, ok := store.memberships[store.key(projectID, actorID)]; ok {
		return membership, nil
	}
	return domain.Membership{}, domain.E(domain.KindNotFound, "membership_not_found", "actor_id", "not a member")
}

func (store *fakeStore) FindActiveGrant(sourceID, ownerProjectID, granteeProjectID string) (domain.Grant, error) {
	for _, grant := range store.grants {
		if grant.State == domain.GrantActive && grant.SourceID == sourceID &&
			grant.OwnerProjectID == ownerProjectID && grant.GranteeProjectID == granteeProjectID {
			return grant, nil
		}
	}
	return domain.Grant{}, domain.E(domain.KindNotFound, "grant_not_found", "grant_id", "grant does not exist")
}

func (store *fakeStore) GrantIsActive(grantID, sourceID, ownerProjectID, granteeProjectID string) (domain.Grant, error) {
	grant, ok := store.grants[grantID]
	if !ok {
		return domain.Grant{}, domain.E(domain.KindNotFound, "grant_not_found", "grant_id", "grant does not exist")
	}
	if grant.State == domain.GrantActive && grant.SourceID == sourceID &&
		grant.OwnerProjectID == ownerProjectID && grant.GranteeProjectID == granteeProjectID {
		return grant, nil
	}
	return domain.Grant{}, domain.E(domain.KindForbidden, "authorization_revoked", "grant_id", "grant no longer active")
}

func seededStore() *fakeStore {
	store := newFakeStore()
	store.projects["proj_a"] = domain.Project{ID: "proj_a"}
	store.projects["proj_b"] = domain.Project{ID: "proj_b"}
	store.memberships[store.key("proj_a", "owner_a")] = domain.Membership{ProjectID: "proj_a", ActorID: "owner_a", Role: domain.RoleOwner}
	store.memberships[store.key("proj_a", "editor_a")] = domain.Membership{ProjectID: "proj_a", ActorID: "editor_a", Role: domain.RoleEditor}
	store.memberships[store.key("proj_a", "viewer_a")] = domain.Membership{ProjectID: "proj_a", ActorID: "viewer_a", Role: domain.RoleViewer}
	store.memberships[store.key("proj_b", "owner_b")] = domain.Membership{ProjectID: "proj_b", ActorID: "owner_b", Role: domain.RoleOwner}
	store.grants["grant_1"] = domain.Grant{
		ID: "grant_1", SourceID: "src_a", OwnerProjectID: "proj_a",
		GranteeProjectID: "proj_b", State: domain.GrantActive,
	}
	return store
}

func TestRolePermissions(t *testing.T) {
	cases := []struct {
		role     domain.Role
		required domain.Role
		want     bool
	}{
		{domain.RoleOwner, domain.RoleOwner, true},
		{domain.RoleOwner, domain.RoleEditor, true},
		{domain.RoleOwner, domain.RoleViewer, true},
		{domain.RoleEditor, domain.RoleOwner, false},
		{domain.RoleEditor, domain.RoleEditor, true},
		{domain.RoleViewer, domain.RoleEditor, false},
		{domain.RoleViewer, domain.RoleViewer, true},
		{domain.Role(""), domain.RoleViewer, false},
	}
	for _, tc := range cases {
		if got := tc.role.Permits(tc.required); got != tc.want {
			t.Errorf("%s.Permits(%s) = %v, want %v", tc.role, tc.required, got, tc.want)
		}
	}
}

func TestAuthenticateHidesProjectExistence(t *testing.T) {
	guard := NewGuard(seededStore())

	// A non-member of an existing project gets the same not-found shape as a
	// request against a missing project.
	_, err := guard.Authenticate(Principal{ProjectID: "proj_a", ActorID: "stranger"})
	if domain.AsAppError(err).Kind != domain.KindNotFound || domain.AsAppError(err).Detail.Code != "project_not_found" {
		t.Fatalf("non-member auth error = %v, want not_found/project_not_found", err)
	}
	_, err = guard.Authenticate(Principal{ProjectID: "proj_missing", ActorID: "owner_a"})
	if domain.AsAppError(err).Kind != domain.KindNotFound || domain.AsAppError(err).Detail.Code != "project_not_found" {
		t.Fatalf("missing-project auth error = %v, want not_found/project_not_found", err)
	}
	_, err = guard.Authenticate(Principal{})
	if domain.AsAppError(err).Kind != domain.KindUnauthorized {
		t.Fatalf("empty principal error = %v, want unauthorized", err)
	}
}

func TestRequireRole(t *testing.T) {
	guard := NewGuard(seededStore())
	if _, err := guard.RequireRole(Principal{ProjectID: "proj_a", ActorID: "viewer_a"}, domain.RoleEditor); err == nil {
		t.Fatal("viewer was allowed an editor action")
	} else if domain.AsAppError(err).Kind != domain.KindForbidden {
		t.Fatalf("viewer editor action error = %v, want forbidden", err)
	}
	if _, err := guard.RequireRole(Principal{ProjectID: "proj_a", ActorID: "editor_a"}, domain.RoleEditor); err != nil {
		t.Fatalf("editor editor action failed: %v", err)
	}
}

func TestAuthorizeSourceReadOwnershipAndGrant(t *testing.T) {
	guard := NewGuard(seededStore())
	owned := domain.SourceAsset{ID: "src_a", ProjectID: "proj_a"}

	// Owner reads its own source without a grant.
	basis, err := guard.AuthorizeSourceRead(Principal{ProjectID: "proj_a", ActorID: "owner_a"}, owned)
	if err != nil {
		t.Fatalf("owner read failed: %v", err)
	}
	if basis.AccessBasis != domain.AccessBasisOwned || basis.GrantID != "" {
		t.Fatalf("owner basis = %+v, want owned without grant", basis)
	}

	// Grantee reads through the active grant.
	basis, err = guard.AuthorizeSourceRead(Principal{ProjectID: "proj_b", ActorID: "owner_b"}, owned)
	if err != nil {
		t.Fatalf("grantee read failed: %v", err)
	}
	if basis.AccessBasis != domain.AccessBasisGrant || basis.GrantID != "grant_1" || basis.OwnerProjectID != "proj_a" {
		t.Fatalf("grantee basis = %+v, want grant_1 basis", basis)
	}

	// A third project without a grant receives source_not_found, never a
	// permission hint that would disclose existence.
	store := guard.store.(*fakeStore)
	store.projects["proj_c"] = domain.Project{ID: "proj_c"}
	store.memberships[store.key("proj_c", "owner_c")] = domain.Membership{ProjectID: "proj_c", ActorID: "owner_c", Role: domain.RoleOwner}
	_, err = guard.AuthorizeSourceRead(Principal{ProjectID: "proj_c", ActorID: "owner_c"}, owned)
	if appErr := domain.AsAppError(err); appErr.Kind != domain.KindNotFound || appErr.Detail.Code != "source_not_found" {
		t.Fatalf("ungranted read error = %v, want not_found/source_not_found", err)
	}
}

func TestVerifyPublicationRevocation(t *testing.T) {
	store := seededStore()
	guard := NewGuard(store)
	grantee := Principal{ProjectID: "proj_b", ActorID: "owner_b"}
	grantBasis := domain.SourceAuthorization{
		SourceID: "src_a", OwnerProjectID: "proj_a", GrantID: "grant_1",
		AccessBasis: domain.AccessBasisGrant, GranteeProjectID: "proj_b",
	}
	ownedBasis := domain.SourceAuthorization{
		SourceID: "src_b", OwnerProjectID: "proj_b", AccessBasis: domain.AccessBasisOwned,
	}

	if err := guard.VerifyPublication(grantee, []domain.SourceAuthorization{ownedBasis, grantBasis}); err != nil {
		t.Fatalf("publication under active grant failed: %v", err)
	}

	// Revoking the grant must fail publication for any artifact that still
	// carries that basis.
	store.grants["grant_1"] = domain.Grant{
		ID: "grant_1", SourceID: "src_a", OwnerProjectID: "proj_a",
		GranteeProjectID: "proj_b", State: domain.GrantRevoked,
	}
	err := guard.VerifyPublication(grantee, []domain.SourceAuthorization{ownedBasis, grantBasis})
	if appErr := domain.AsAppError(err); appErr.Kind != domain.KindForbidden || appErr.Detail.Code != "authorization_revoked" {
		t.Fatalf("post-revocation publication error = %v, want forbidden/authorization_revoked", err)
	}

	// A render using only owned sources still publishes.
	if err := guard.VerifyPublication(grantee, []domain.SourceAuthorization{ownedBasis}); err != nil {
		t.Fatalf("owned-only publication failed after unrelated revocation: %v", err)
	}
}

func TestAuthorizeResourceReadIsProjectScoped(t *testing.T) {
	err := AuthorizeResourceRead(
		Principal{ProjectID: "proj_a", ActorID: "owner_a"},
		"proj_b", "render_x", "render_not_found", "render_id", "hidden",
	)
	if appErr := domain.AsAppError(err); appErr.Kind != domain.KindNotFound || appErr.Detail.Code != "render_not_found" {
		t.Fatalf("cross-project read error = %v, want not_found/render_not_found", err)
	}
}
