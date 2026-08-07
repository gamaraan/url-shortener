# URL Shortener — Requirements

## Core API

Design an API that will, at a minimum, allow the user to submit any URL to be
shortened and receive a valid shortened URL that will forward the user's request
to the original URL.

The API should accept a `PUT` request with the following JSON data:

```json
{
  "destination": "valid url"
}
```

and return the shortened URL or error as JSON.

The service should accept a request at `/:shortcode:` and redirect the user to
the original URL or return an error message as JSON.

## Implementation

- Develop API using **Go**.
- Implement unit tests for the API.
- Commit work to feature / fixes branches
- Document project in a `README.md`
- Document progress in a `CHANGELOG.md`
