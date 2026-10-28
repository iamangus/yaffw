# Omniarr Library API Reference

This document details the API endpoints available for consuming the Omniarr library data. These endpoints are designed to be used by external frontend applications (like a Jellyfin replacement) to build media interfaces.

## Authentication

All requests typically require an API Key if authentication is enabled.
Header: `X-API-Key: <your_api_key>`

## 1. List Entities (Library Browsing)

Retrieve a list of entities from the database. This endpoint supports filtering to allow drilling down into the library hierarchy.

**Endpoint:** `GET /entities`

### Query Parameters

| Parameter | Type   | Description                                                                 | Example                                |
| :-------- | :----- | :-------------------------------------------------------------------------- | :------------------------------------- |
| `type`    | string | Filter by `entity_type`. Common types: `movie`, `series`, `season`, `episode`. | `type=series`                          |
| `parent`  | string | Filter by `parent_uuid`. Used to get children of a specific entity.         | `parent=550e8400-e29b...`              |
| `status`  | string | Filter by status. Useful for showing only available content.                | `status=DOWNLOADED`                    |

### Usage Scenarios

#### Get All TV Shows
Retrieve the root list of TV shows to display on the library dashboard.
```http
GET /entities?type=series
```

#### Get Seasons for a Show
When a user clicks on a show, fetch the seasons.
```http
GET /entities?type=season&parent={series_uuid}
```

#### Get Episodes for a Season
When a user expands a season, fetch the episodes.
```http
GET /entities?type=episode&parent={season_uuid}
```

#### Get All Movies
Retrieve the list of movies.
```http
GET /entities?type=movie
```

#### Get Only Available Content
Filter to show only items that are ready to play.
```http
GET /entities?type=movie&status=DOWNLOADED
```

## 2. Get Entity Details (Item Lookup)

Retrieve full details for a specific entity. This is critical for getting the `local_path` needed for playback/transcoding.

**Endpoint:** `GET /entities/:uuid`

### Path Parameters

| Parameter | Type   | Description                       |
| :-------- | :----- | :-------------------------------- |
| `uuid`    | string | The unique identifier of the entity. |

### Response Example

```json
{
  "uuid": "550e8400-e29b-41d4-a716-446655440000",
  "entity_type": "movie",
  "status": "DOWNLOADED",
  "local_path": "/mnt/media/movies/The Matrix (1999)/The Matrix.mkv",
  "image_path": "/home/omniarr/images/movie_123.jpg",
  "metadata": {
    "title": "The Matrix",
    "year": "1999",
    "description": "Welcome to the Real World.",
    "image": "https://image.tmdb.org/t/p/original/..."
  }
}
```

### Integration Notes for Playback
1.  Frontend fetches the entity details: `GET /entities/{uuid}`.
2.  Frontend extracts `local_path` from the response.
3.  Frontend sends `local_path` to the Transcoding Service to initiate the stream.

## 3. Serving Images

Omniarr downloads and stores images locally. The frontend can serve these directly from Omniarr.

**Endpoint:** `GET /images/:filename`

The filename is available in the `image_path` field of the Entity response (or usually derived from metadata extra fields depending on implementation details, but `GetEntity` response includes `image_path` which points to the local file reference).

*Note: The `image_path` field in the DB might store the absolute path or relative filename depending on internal logic. The API serves the configured image directory.*
