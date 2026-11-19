package main

import (
	"context"
	"fmt"
	"sort"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// readOnlyActions are the only MongoDB privilege actions a shadow user may
// hold. Anything else (insert, update, remove, createIndex, dropCollection,
// ...) makes the shadow refuse to start: the guarantee "the shadow never
// writes" must hold on the server too, not only in this code.
var readOnlyActions = map[string]bool{
	"find": true, "listCollections": true, "listIndexes": true, "collStats": true,
	"dbStats": true, "killCursors": true, "listDatabases": true, "changeStream": true,
	"dbHash": true, "planCacheRead": true,
}

type readOnlyProof struct {
	Users   []string `json:"users"`
	Roles   []string `json:"roles"`
	Actions []string `json:"actions"`
}

// proveReadOnly asks the server which privileges the connection holds
// (connectionStatus with showPrivileges, which needs no privilege itself) and
// fails unless there is an authenticated user and every action is read only.
func proveReadOnly(ctx context.Context, client *mongo.Client) (readOnlyProof, error) {
	var res struct {
		AuthInfo struct {
			Users []struct {
				User string `bson:"user"`
				DB   string `bson:"db"`
			} `bson:"authenticatedUsers"`
			Roles []struct {
				Role string `bson:"role"`
				DB   string `bson:"db"`
			} `bson:"authenticatedUserRoles"`
			Privileges []struct {
				Actions []string `bson:"actions"`
			} `bson:"authenticatedUserPrivileges"`
		} `bson:"authInfo"`
	}
	err := client.Database("admin").RunCommand(ctx, bson.D{{Key: "connectionStatus", Value: 1}, {Key: "showPrivileges", Value: true}}).Decode(&res)
	if err != nil {
		return readOnlyProof{}, fmt.Errorf("connectionStatus: %w", err)
	}
	var p readOnlyProof
	for _, u := range res.AuthInfo.Users {
		p.Users = append(p.Users, u.User+"@"+u.DB)
	}
	for _, r := range res.AuthInfo.Roles {
		p.Roles = append(p.Roles, r.Role+"@"+r.DB)
	}
	if len(p.Users) == 0 {
		return p, fmt.Errorf("no authenticated user: read-only access cannot be proven (use a dedicated read-only database user)")
	}
	seen := map[string]bool{}
	var bad []string
	for _, pr := range res.AuthInfo.Privileges {
		for _, a := range pr.Actions {
			if seen[a] {
				continue
			}
			seen[a] = true
			p.Actions = append(p.Actions, a)
			if !readOnlyActions[a] {
				bad = append(bad, a)
			}
		}
	}
	sort.Strings(p.Actions)
	sort.Strings(bad)
	if len(p.Actions) == 0 {
		return p, fmt.Errorf("the user holds no privilege at all")
	}
	if len(bad) > 0 {
		return p, fmt.Errorf("the user can do more than read: %v", bad)
	}
	return p, nil
}
