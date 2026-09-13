//go:build !windows

package appcomponent

import appbackend "github.com/ingot-agent/plugins/app-webui"

func workspaceRootEntries(string) []appbackend.WorkspaceBrowseEntry {
	return nil
}
