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

- Develop your API using **Go**. We value good unit tests.
- Commit your work as you go in a git repository. This repository can be local
  or hosted, but we would like to see your commit history.
- Document your project in a `README.md` markdown file as part of your
  repository.
