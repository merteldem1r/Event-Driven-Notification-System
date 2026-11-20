package handler

import (
	"net/http"
)

// swaggerUIVersion is the Swagger UI release loaded from the CDN. The integrity hashes in
// swaggerPage belong to this version: change them together.
const swaggerUIVersion = "5.17.14"

// swaggerPage loads Swagger UI from jsDelivr and points it at /openapi.yaml. The integrity
// hashes make the browser refuse the files if the CDN ever serves anything else.
const swaggerPage = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Notification Service API</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@` + swaggerUIVersion + `/swagger-ui.css"
        integrity="sha384-wxLW6kwyHktdDGr6Pv1zgm/VGJh99lfUbzSn6HNHBENZlCN7W602k9VkGdxuFvPn" crossorigin="anonymous">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@` + swaggerUIVersion + `/swagger-ui-bundle.js"
          integrity="sha384-wmyclcVGX/WhUkdkATwhaK1X1JtiNrr2EoYJ+diV3vj4v6OC5yCeSu+yW13SYJep" crossorigin="anonymous"></script>
  <script>
    window.ui = SwaggerUIBundle({ url: "/openapi.yaml", dom_id: "#swagger-ui", deepLinking: true });
  </script>
</body>
</html>
`

// OpenAPISpec serves the API specification as YAML.
func OpenAPISpec(spec []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(spec) // the client went away; nothing to do
	}
}

// SwaggerUI serves the interactive API documentation page.
func SwaggerUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(swaggerPage)) // the client went away; nothing to do
}
