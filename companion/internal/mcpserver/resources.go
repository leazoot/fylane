package mcpserver

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// resourceScheme addresses workspace files as MCP resources:
// fylane://<workspace_id>/<relative-path>[?start_line=N&end_line=M].
// Truncated tool results carry such a URI so clients that support
// resources/read can page through content the inline budget cut off.
const resourceScheme = "fylane"

func fileResourceURI(workspaceID, canonical string) string {
	u := url.URL{Scheme: resourceScheme, Host: workspaceID, Path: "/" + canonical}
	return u.String()
}

// readResource serves resources/read for workspace files. It goes through
// the exact same funnel as read_file — sandbox, exclude and sensitive rules,
// binary handling, and the inline budget all apply; a resource URI is never
// a way around them.
func (t *toolset) readResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	uri := req.Params.URI
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != resourceScheme || u.Host == "" {
		return nil, mcp.ResourceNotFoundError(uri)
	}
	rel := strings.TrimPrefix(u.Path, "/")
	if rel == "" {
		return nil, mcp.ResourceNotFoundError(uri)
	}
	start, err := queryLine(u, "start_line")
	if err != nil {
		return nil, err
	}
	end, err := queryLine(u, "end_line")
	if err != nil {
		return nil, err
	}

	ws, err := t.open(ctx, u.Host)
	if err != nil {
		return nil, err
	}
	out, err := t.readOne(ctx, ws, rel, start, end)
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
		URI:      uri,
		MIMEType: "text/plain",
		Text:     out.Content,
	}}}, nil
}

func queryLine(u *url.URL, key string) (int, error) {
	raw := u.Query().Get(key)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, mcp.ResourceNotFoundError(u.String())
	}
	return n, nil
}
