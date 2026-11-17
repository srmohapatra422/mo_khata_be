# Mo Hisab API — request and response reference

Base URL (live): `https://mohisab-api.onrender.com` — local: `http://localhost:8080`
Health check: `GET /health` returns `{"status":"ok"}`. The free Render plan sleeps when idle, so the first call can take up to a minute.
All bodies are JSON (`Content-Type: application/json`). All timestamps are RFC 3339. All ids are UUIDs. All money is a plain number in rupees.

## Conventions

### Headers

| Header | Needed on | Value |
|---|---|---|
| `Authorization` | every `/v1/*` route | `Bearer <access_token>` |
| `X-Shop-ID` | routes marked **shop** | the shop's id. The user must be a member of that shop |
| `Content-Type` | every request with a body | `application/json` |

### Errors

Every error has the same shape:

```json
{ "error": "message" }
```

| Status | Meaning |
|---|---|
| 400 | Bad body, missing or invalid field, missing `X-Shop-ID`. Unknown JSON fields are rejected. Max body 1 MB |
| 401 | Missing, expired or invalid token. Or wrong OTP code |
| 403 | Not a member of the shop, the role is not allowed, or the customer phone is not verified |
| 404 | The item, reward or stock row does not exist in this shop |
| 409 | Business rule failed: duplicate name, not enough points, day already closed, and similar |
| 500 | Database error |
| 502 | OTP provider failed |

### Shop timezone

Every "today", "this week", "this month" and "yesterday" in this API uses the **shop's timezone** (default `Asia/Kolkata`), never UTC. Weeks start on Monday.

### Retrying safely: `Idempotency-Key`

`POST /v1/bills`, `POST /v1/stock/in` and `POST /v1/close-day` accept an optional header `Idempotency-Key: <any unique text, up to 128 characters>`. If the app sends the same key again for the same shop, the server returns the **first successful response** again (with the header `Idempotent-Replay: true`) and does nothing twice. Use one new key per user action, and reuse it only when retrying that action after a network failure. Only successful (2xx) responses are remembered, so a request that failed with 400 or 409 can be retried with the same key after fixing it. Using one key on a different path returns `409`.

### Access levels

- **public**: no token.
- **token**: valid `Authorization` header.
- **shop**: token plus `X-Shop-ID`. Any role (`owner`, `cashier`, `farm_manager`).
- **owner**: same as shop, but only the `owner` role.
- **owner, cashier** (and similar): only the listed roles.

### Who can do what

| Area | owner | cashier | farm_manager |
|---|---|---|---|
| Dashboard, setup, rewards, categories, prices (read) | yes | yes | yes (dashboard: farm location only) |
| Create or change price items, save rates | yes | yes | no |
| Bulk ready list, delete price item, create category | yes | no | no |
| Stock: read, add stock | yes | yes | yes, farm location only |
| Stock: wastage | yes | no | yes, farm location only |
| Stock: reorder level | yes | no | no |
| Bills, points OTP | yes | yes | yes |
| Expenses | yes | yes | yes, farm location only |
| Close the day | yes | yes | no |
| Setup tick, rewards change | yes | no | no |
| Voice parse | yes | yes | yes |
| Profile: read, edit own name/photo/language, devices (read) | yes | yes | yes (profile shows the farm only; plan is hidden) |
| Staff list, devices (change) | yes | yes | no |
| Edit shop, categories, locations, offer, staff invite/change/remove, plan, backup, export | yes | no | no |

A farm manager who sends another location, or asks for all locations, only ever gets the farm location. Asking for a shop location returns `403`.

---

## 1. Health

### `GET /health` — public

Response `200`
```json
{ "status": "ok" }
```

---

## 2. Login (DEV ONLY)

These routes exist only while the server has `DEV_AUTH_SECRET` set. Any phone number and any non-empty code logs in. They are replaced by Supabase phone OTP before production, and the `/v1` routes do not change.

### `POST /auth/otp/send` — public

Request
```json
{ "phone": "9000000001" }
```

Response `200`
```json
{ "sent": true, "dev_mode": true }
```
Errors: `400` phone missing.

### `POST /auth/otp/verify` — public

Request
```json
{ "phone": "9000000001", "code": "123456" }
```
`code` can be any non-empty text in dev mode.

Response `200`
```json
{
  "access_token": "eyJhbGciOi...",
  "token_type": "bearer",
  "expires_at": "2026-12-13T15:50:59Z",
  "user_id": "347bb2a3-e506-4ef5-be08-dd44e31d7ade",
  "dev_mode": true
}
```
The token is valid for 30 days. Logging in again with the same phone returns the same `user_id`.

Errors: `400` phone or code missing. `401` wrong code (never in dev mode).

---

## 3. Me and shops

### `GET /v1/me` — token

Returns the user and the shops they belong to, oldest shop first. Each shop has its `locations`, so a returning user already has the `location_id` that bills need. A pending staff invite for this phone is accepted here.

Response `200`
```json
{
  "user_id": "347bb2a3-e506-4ef5-be08-dd44e31d7ade",
  "user": { "id": "347bb2a3-…", "name": "Ravi Kumar", "phone": "9000000001", "photo_url": null, "language": "en" },
  "shops": [
    {
      "id": "ead632e7-8b5f-40f9-bcc6-07cf7e5ff479",
      "name": "My Shop",
      "role": "owner",
      "logo_url": "https://example.com/logo.png",
      "categories": ["chicken", "fish", "eggs"],
      "locations": [
        { "id": "uuid", "name": "Poultry farm", "type": "farm", "lat": 20.29, "lng": 85.82, "active": true },
        { "id": "uuid", "name": "Shop", "type": "shop", "lat": null, "lng": null, "active": true }
      ]
    }
  ]
}
```
`shops` is `[]` for a new user, which means the app should show onboarding. `logo_url` is `null` when no logo was set. A farm manager only sees the farm location. Removed members do not see the shop.

### `PATCH /v1/me` — token

Edit the profile header (name, photo, language). See section 18.

### `POST /v1/shops` — token

Onboarding screen ("Tell us about your business"). Creates the shop, makes the caller its `owner`, and creates the locations the user ticked. The response includes the new locations with their ids, and `POST /v1/bills` needs a `location_id`.

Request
```json
{
  "name": "Soumya Poultry",
  "logo_url": "https://example.com/logo.png",
  "categories": ["chicken", "fish", "eggs", "mutton", "dairy"],
  "has_farm": true,
  "has_shop": true,
  "farm_location": { "lat": 20.2961, "lng": 85.8245 },
  "shop_location": { "lat": 20.3000, "lng": 85.8300 }
}
```

| Screen element | Field | Type | Required | Notes |
|---|---|---|---|---|
| Shop name | `name` | string | yes | trimmed. Must not be empty |
| Shop logo | `logo_url` | string | no | the `url` returned by `POST /v1/uploads/logo`. Once logo upload is enabled on the server, any other address returns 400. Omit if no logo |
| What do you sell? (pick all) | `categories` | array of string | no | allowed: `chicken`, `fish`, `eggs`, `mutton`, `dairy`. "Brown eggs" is sent as `eggs`. Duplicates are ignored. Any other value returns 400 |
| Your locations: Poultry farm | `has_farm` | bool | no | default `false`. Creates a location named "Poultry farm" (type `farm`) |
| Your locations: Shop | `has_shop` | bool | no | default `true`. Creates a location of type `shop` |
| Your locations: farm position | `farm_location` | object `{lat, lng}` | **no, optional** | map position of the farm. Only allowed when `has_farm` is true |
| Your locations: shop position | `shop_location` | object `{lat, lng}` | **no, optional** | map position of the shop. Only allowed when `has_shop` is true |
| (not on screen) | `shop_label` | string | no | name of the shop location. Default "Shop" |

At least one of `has_farm` and `has_shop` must be true. In `farm_location` and `shop_location`, `lat` and `lng` are both required, with `lat` between -90 and 90 and `lng` between -180 and 180. Leave the object out when the user did not pick a position. Sending `null`, `{}` or `{"lat":null,"lng":null}` also counts as no position. You can send a position for the farm only, for the shop only, or for both.

Minimal request without positions:
```json
{ "name": "Soumya Poultry", "has_farm": true, "has_shop": true }
```

Response `201`
```json
{
  "id": "04c868d3-890c-4602-a99b-559ae9308297",
  "name": "Soumya Poultry",
  "logo_url": "https://example.com/logo.png",
  "categories": ["chicken", "fish", "eggs", "mutton", "dairy"],
  "locations": [
    { "id": "9ff14822-d62c-4b62-8282-0be0151efe8e", "name": "Poultry farm", "type": "farm", "lat": 20.2961, "lng": 85.8245 },
    { "id": "08268316-8b91-4a02-87c7-7a35f8ba60da", "name": "Shop", "type": "shop", "lat": 20.3, "lng": 85.83 }
  ]
}
```
`locations` is in screen order: farm first, then shop. `lat` and `lng` are `null` when no position was sent. Keep these ids and use the `shop` location id as `location_id` when saving bills.

Errors
- `400` `name is required`
- `400` `pick at least one location`
- `400` `unknown category: <value>`
- `400` `logo_url must be the url returned by POST /v1/uploads/logo`
- `400` `farm_location needs both lat and lng` (same for `shop_location`)
- `400` `farm_location: lat must be between -90 and 90` (also `lng` between -180 and 180, and `shop_location`)
- `400` `farm_location given but has_farm is false` (same for `shop_location` and `has_shop`)

Shop defaults: 1 point per ₹10 spent, 1 point = ₹0.25, points expire after 12 months, at most 50% of a bill can be paid with points, language `or`.

Logo: upload the image first with `POST /v1/uploads/logo` and send the returned URL as `logo_url`.

---|---|---|---|
| `name` | string | yes | |
| `has_farm` | bool | no | default `false`. Adds a location named "Poultry farm" |
| `shop_label` | string | no | default `"Main shop"`. Name of the shop location |

Response `201`
```json
{ "id": "ead632e7-8b5f-40f9-bcc6-07cf7e5ff479", "name": "My Shop" }
```
Errors: `400` name missing.

Shop defaults: 1 point per ₹10 spent, 1 point = ₹0.25, points expire after 12 months, at most 50% of a bill can be paid with points, language `or`.

---

## 4. Logo upload

### `POST /v1/uploads/logo` — token

Uploads the shop logo and returns its URL. Use it on the onboarding screen **before** `POST /v1/shops`, because the shop does not exist yet. It needs only the token, not `X-Shop-ID`.

Request: `multipart/form-data`

| Parameter | Type | Required | Notes |
|---|---|---|---|
| `file` | file | yes | PNG, JPEG or WebP, up to 5 MB. The type is checked from the file contents, not the file name |

Both gallery and camera images work. The app should shrink the image first (for example 512 px wide, quality 85), so the upload is fast.

Response `201`
```json
{ "url": "https://<project>.supabase.co/storage/v1/object/public/shop-logos/<user-id>/<id>.png" }
```
Send this `url` as `logo_url` in `POST /v1/shops`. The URL is public, so the app can show it in an image widget directly.

Errors
- `400` no file in the field `file`, or an empty file
- `401` missing or invalid token
- `413` image larger than 5 MB
- `415` not a PNG, JPEG or WebP image
- `502` the image could not be stored
- `503` logo upload is not configured on the server

Flutter (Dio)
```dart
final form = FormData.fromMap({
  'file': await MultipartFile.fromFile(img.path, filename: 'logo.jpg'),
});
final res = await dio.post('$apiBase/v1/uploads/logo', data: form,
    options: Options(headers: {'Authorization': 'Bearer $token'}));
final logoUrl = res.data['url'];
```

---

## 5. Items and prices (original endpoints)

These still work as before. The newer endpoints in section 9 (`/v1/prices`, `/v1/prices/items`, categories) work on the same items, so an item added with either appears in both.

### `GET /v1/prices/today` — shop

Active items with today's price. If no price is set for today, the most recent earlier price is returned. If there is none, `price_today` is `null`. Sorted by category, then name.

Response `200`
```json
[
  {
    "id": "b064cd3e-06cd-4964-95dc-69eb2b22ff9b",
    "category": "chicken",
    "name": "Broiler · Boneless",
    "unit": "kg",
    "price_today": 320,
    "price_yesterday": 315
  }
]
```
`price_yesterday` is the latest price before today, or `null`. It drives the "compare with yesterday" switch.

### `POST /v1/items` — owner

Adds an item, optionally with today's price.

Request
```json
{ "category": "chicken", "name": "Broiler · Boneless", "unit": "kg", "price": 320 }
```

| Field | Type | Required | Allowed values |
|---|---|---|---|
| `category` | string | yes | `chicken`, `fish`, `mutton`, `eggs`, `dairy`, `other` |
| `name` | string | yes | |
| `unit` | string | yes | `kg`, `piece`, `tray`, `dozen`, `bird` |
| `price` | number | no | sets today's price. Must be 0 or more |

Response `201`
```json
{ "id": "b064cd3e-06cd-4964-95dc-69eb2b22ff9b" }
```
Errors: `400` a required field is missing or a value is not allowed.

### `PUT /v1/prices/today` — owner

Sets today's price for several items in one call. If a price already exists for today, it is replaced. The request body is a list, not an object.

Request
```json
[
  { "item_id": "b064cd3e-06cd-4964-95dc-69eb2b22ff9b", "price": 330 },
  { "item_id": "7f3c1e52-0a41-4d0c-9a56-1c2b5d8e9f10", "price": 180.5 }
]
```

Response `200`
```json
{ "updated": 2 }
```
Errors: `400` empty list, or an item id that does not belong to this shop. Nothing is saved if any item fails.

---

## 6. Customers

### `GET /v1/customers?q=` — shop

Searches this shop's customers by phone or name (case-insensitive, partial match). Returns up to 50, newest first. Without `q`, it returns the 50 newest.

Example: `GET /v1/customers?q=9848`

Response `200`
```json
[
  { "id": "76763ee2-8937-4872-a9a7-780abcb88c0b", "phone": "9848012345", "name": null, "points": 85 }
]
```
`points` is the customer's current points balance. `name` can be `null`. Customers are created automatically on their first bill.

---

## 7. Bills

### `POST /v1/bills` — shop

Saves a bill. In one transaction it:
1. Recalculates all totals on the server. Totals sent by the app are ignored.
2. Creates the customer if the phone is new.
3. Applies the points discount, and earns new points.
4. Writes the bill lines.
5. Writes one stock-out row for each line that has an `item_id`.

The bill `id` is made by the app (UUID v7). Sending the same `id` again returns the saved bill, so retrying after a network drop is safe.

Request
```json
{
  "id": "0193a1e2-7c4b-7d11-9a2e-5b8f3c1d2e4f",
  "location_id": "c0a8e1d2-3b4f-4a5c-8d6e-7f8091a2b3c4",
  "customer_phone": "9848012345",
  "payment_mode": "upi",
  "points_used": 0,
  "created_at": "2026-11-13T12:30:00+05:30",
  "lines": [
    { "item_id": "b064cd3e-06cd-4964-95dc-69eb2b22ff9b", "name": "Broiler · Boneless", "qty": 1.5, "rate": 320 },
    { "name": "Mutton", "qty": 0.5, "rate": 750, "note": "curry pieces" }
  ]
}
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `id` | uuid | yes | made on the phone |
| `location_id` | uuid | yes | must be a location of this shop |
| `customer_phone` | string | no | empty or missing means a walk-in. Walk-ins cannot use points |
| `payment_mode` | string | yes | `cash`, `upi`, `card`, `credit` |
| `points_used` | int | no | default 0. See the rules below |
| `created_at` | timestamp | no | time on the phone. Defaults to now |
| `lines` | array | yes | at least one line |

Line fields

| Field | Type | Required | Notes |
|---|---|---|---|
| `item_id` | uuid or null | no | omit for a manual entry. Only lines with an item reduce stock |
| `name` | string | yes | not empty |
| `qty` | number | yes | greater than 0, up to 3 decimals |
| `rate` | number | yes | 0 or more |
| `price_edited` | bool | no | true if the cashier changed the shown price |
| `note` | string | no | |
| `unit` | string | no | `kg`, `bird`, `tray` or `piece`. Used to find the matching stock item. Taken from the price item when `item_id` is sent |

Response `201` (new bill)
```json
{
  "id": "0193a1e2-7c4b-7d11-9a2e-5b8f3c1d2e4f",
  "total": 855,
  "points_discount": 0,
  "payable": 855,
  "points_earned": 85,
  "points_balance": 85,
  "already_saved": false
}
```

Response `200` (same `id` sent again). It returns the saved bill, and `points_balance` is `null`.
```json
{
  "id": "0193a1e2-7c4b-7d11-9a2e-5b8f3c1d2e4f",
  "total": 855,
  "points_discount": 0,
  "payable": 855,
  "points_earned": 85,
  "points_balance": null,
  "already_saved": true
}
```

| Response field | Meaning |
|---|---|
| `total` | sum of `qty × rate` for all lines, each line rounded to 2 decimals |
| `points_discount` | `points_used × point value` (₹0.25 by default) |
| `payable` | `total − points_discount` |
| `points_earned` | `floor(payable ÷ rupees per point)`. Customers only |
| `points_balance` | the customer's balance after this bill. `null` for walk-ins and repeated sends |

Stock: for each line, the sold quantity is taken off the stock item with the same name (spelling variants count: `Eggs`, `egg`, `anda`) at the bill's location, in the same unit when the unit is known. It never goes below 0, and a bill is never refused because of stock. A line that matches no stock item changes nothing. The same bill id sent twice reduces stock once. `POST /v1/bills` also accepts `Idempotency-Key`.

Points rules
- Redeeming (`points_used > 0`) needs a customer phone that was verified with the OTP endpoints below. Each verification works for one bill.
- `points_used` cannot be more than the customer's balance.
- The discount cannot be more than the shop's maximum share of the bill (50% by default).

Errors
- `400`: bad body, missing field, unknown location, invalid line, `payment_mode` not allowed, or a walk-in bill with points.
- `403`: `customer phone not verified: send and verify OTP first`.
- `409`: `not enough points`, or `points exceed the allowed share of this bill`.

---

## 8. Customer points OTP

Used before redeeming points. Both routes need the **shop** headers. In dev mode no SMS is sent and any non-empty code passes.

### `POST /v1/points/otp/send` — shop

Request
```json
{ "phone": "9848012345" }
```

Response `200`
```json
{ "sent": true, "dev_mode": true }
```
Errors: `400` phone missing. `502` provider failed.

### `POST /v1/points/otp/verify` — shop

Request
```json
{ "phone": "9848012345", "code": "123456" }
```

Response `200`
```json
{ "verified": true, "valid_until": "2026-11-13T15:45:22+05:30", "dev_mode": true }
```
The verification lasts 10 minutes and is used up by the next bill that redeems points for that phone.

Errors: `400` phone or code missing. `401` wrong code. `502` provider failed.

---

## 9. Categories and prices

Standard categories are `chicken`, `fish`, `eggs`, `mutton` and `dairy`. A shop can also add its own (for example "Ready to cook"). Price units here are `kg`, `tray`, `piece` and `bird`.

Names are compared in a forgiving way when checking duplicates: upper or lower case, extra spaces, a trailing "s", and common Hindi or Odia words all count as the same. For example `Eggs`, `egg` and `anda` are the same name, and `machhi` means `fish`.

### `GET /v1/categories` — shop

Response `200`: the five standard categories first, then custom ones.
```json
[
  { "id": "10708696-e9a9-42fa-9fc9-b33923b889f0", "key": "fish", "name": "Fish", "kind": "standard" },
  { "id": "3a6cad8d-c82a-4106-bc89-191c37bbe501", "key": null, "name": "Ready to cook", "kind": "custom" }
]
```

### `POST /v1/categories` — owner

Request `{ "name": "Ready to cook" }`. Response `201` is the new category (`kind` is `custom`).
Errors: `400` name is required (up to 60 characters). `409` `category already exists`, which also happens when the name is a standard one in another spelling (`Eggs`, `anda`).

### `GET /v1/prices` — shop

All active items with today's and yesterday's price.
```json
{
  "date": "2026-11-16",
  "updated_today": true,
  "opted_in_customers": 312,
  "items": [
    {
      "id": "09ba242d-cb2c-4c7b-aa69-1d47296e711f",
      "category": { "id": "10708696-e9a9-42fa-9fc9-b33923b889f0", "key": "fish", "name": "Fish" },
      "name": "Katla", "unit": "kg",
      "price": 170, "yesterday": 165, "change": 5
    }
  ]
}
```
- `price` is today's price. If none was saved today, it is the latest earlier price, or `null` if there never was one.
- `yesterday` is the latest price saved before today, or `null`.
- `change` is `price - yesterday`, or `null` when either is missing.
- `updated_today` is true when at least one price was saved today.
- `opted_in_customers` is the number of customers who agreed to WhatsApp messages.
- Items are sorted by category (standard first), then name.

### `POST /v1/prices/items` — owner, cashier

Request
```json
{ "category_id": "10708696-e9a9-42fa-9fc9-b33923b889f0", "name": "Katla", "unit": "kg", "price": 170 }
```
Send `category_name` (for example `"Ready to cook"`) instead of `category_id` to use or create a category by name. `price` is required and saved as today's price. Response `201` is the item, in the same shape as in `GET /v1/prices`. When the item is in a standard category, that category is added to the shop's `categories` (see `GET /v1/me`).
Errors: `400` `category_id or category_name is required`, `name is required`, `name is too long`, `unit must be kg, tray, piece or bird`, `price is required and must be more than 0`, `unknown category`. `409` `item already exists: Katla` (same category, same name).

### `POST /v1/prices/items/bulk` — owner

The "Start from a ready list" screen. Request `{ "items": [ ...same fields as above... ] }` with 1 to 200 items. All or nothing: if any item fails, nothing is saved.
Response `201`: `{ "created": 2, "items": [ ... ] }`.
Errors: `400` `item 2: price is required and must be more than 0` (the number is the position in the list), `409` `item already exists: <name>`, which includes the same name twice in one request. The server creates no default items of its own.

### `PUT /v1/prices` — owner, cashier

"Save today's rates". Request
```json
{ "prices": [ { "item_id": "09ba242d-cb2c-4c7b-aa69-1d47296e711f", "price": 175 } ], "send_whatsapp": true }
```
Replaces today's price for each item in one go. Response `200` is the same body as `GET /v1/prices`. With `send_whatsapp: true` the rate list is queued in the outbox for the customers who agreed to WhatsApp messages. Nothing is sent yet, because the sender is not built.
Errors: `400` `send a list of {item_id, price} in prices`, `every price must be more than 0`, `unknown item <id>` (nothing is saved).

### `POST /v1/prices/keep-yesterday` — owner, cashier

Copies the latest earlier price to today for every active item that has no price today. No body. Response `200` is the same body as `GET /v1/prices`.

### `PATCH /v1/prices/items/{id}` — owner, cashier

Request: any of `{ "name": "Katla large", "unit": "piece", "category": "fish", "active": true }` (`category_id` also works; see section 18). Response `200` is the item. Renaming checks for duplicates again.
Errors: `400` `send name, unit, category or active`, `name must be 1 to 80 characters`, `unit must be kg, tray, piece or bird`. `404` `item not found`. `409` `item already exists: <name>`.

### `DELETE /v1/prices/items/{id}` — owner

Hides the item (price history is kept). Response `204` with no body. `404` `item not found`.

---

## 10. Name check for autocomplete

### `GET /v1/items/match?name=Broilar&scope=price|stock&category_id=&location_id=` — shop

Use it while the user types, and for "Did you mean...?".

| Parameter | Required | Notes |
|---|---|---|
| `name` | no | what was typed. Empty returns the first 6 items |
| `scope` | yes | `price` (price items) or `stock` (stock items) |
| `category_id` | no | `price` scope only: look inside one category |
| `location_id` | no | `stock` scope only: look inside one location |

Response `200`
```json
{
  "kind": "near",
  "item": { "id": "a1745d2e-7514-47f9-a417-3716581fd734", "name": "Broiler", "unit": "kg" },
  "suggestions": [ { "id": "a1745d2e-7514-47f9-a417-3716581fd734", "name": "Broiler", "unit": "kg" } ]
}
```
- `kind` is `exact` (same name, spelling variants included), `near` (a small typo away) or `none`.
- `item` is the best match, or `null` when `kind` is `none`.
- `suggestions` has at most 5 items: names that start with, contain or nearly match the text.
- Typos allowed: none for names under 4 letters, 1 for 4 to 7 letters, 2 for 8 letters or more.

Errors: `400` `scope must be price or stock`, `invalid category_id`, `invalid location_id`.
Creating an item with an exact duplicate name is always refused by the server with `409`, whatever the app checked first.

---

## 11. Stock

Stock units are `kg`, `birds`, `trays` and `pieces`. The app may also send `kilo`, `bird`, `tray`, `piece` or `pcs`. Each item is kept per location and per unit: "Broiler" in kg and "Broiler" in pieces are two rows.

### `GET /v1/stock?location_id=&search=` — shop

```json
{
  "items": [
    {
      "id": "a1745d2e-7514-47f9-a417-3716581fd734",
      "location_id": "c6f2a7b2-1c99-4bf8-a351-00974a9bc286",
      "name": "Broiler", "quantity": 19.5, "unit": "kg",
      "reorder_level": 25, "is_low": true,
      "last_in_at": "2026-11-16T19:32:55+05:30", "last_in_quantity": 7.5
    }
  ]
}
```
- Without `location_id`, all locations are returned (a farm manager only gets the farm).
- `search` tolerates typos and spelling variants (`broilar` finds `Broiler`, `anda` finds `Eggs`).
- `is_low` is true when `reorder_level` is set and `quantity <= reorder_level`.

### `POST /v1/stock/in` — all roles (farm manager: farm location only)

The "Add stock" form and the voice save. Supports `Idempotency-Key`. One transaction.
```json
{
  "location_id": "c6f2a7b2-1c99-4bf8-a351-00974a9bc286",
  "source": "form",
  "items": [ { "name": "Broiler", "unit": "kg", "quantity": 12.5, "rate": 150 } ]
}
```
| Field | Required | Notes |
|---|---|---|
| `location_id` | yes | |
| `source` | no | `form` (default) or `voice` |
| `items` | yes | 1 to 100 rows |
| `items[].name` | yes | |
| `items[].unit` | yes | `kg`, `birds`, `trays`, `pieces` |
| `items[].quantity` | yes | more than 0 |
| `items[].rate` | no | purchase price, 0 or more |

For each row, a stock item with the same name and unit at that location gets the quantity added and **keeps its first spelling**. Otherwise a new stock item is created.
Response `200`: `{ "saved": 1, "items": [ ...updated stock items as in GET /v1/stock... ] }`.
Errors: `400` `location_id is required`, `send between 1 and 100 items`, `item 2: quantity must be more than 0`, `item 1: unit must be kg, birds, trays or pieces`, `unknown location`. `403` `farm managers can only use the farm location`.

### `PATCH /v1/stock/{id}` — owner

Request `{ "reorder_level": 5 }`. Send `null` to clear it. Response `200` is the stock item. `404` `stock item not found`.

### `POST /v1/stock/{id}/wastage` — owner, farm manager (farm location only)

Request `{ "quantity": 0.3, "note": "trim" }`. Response `200` is the stock item with the lower quantity.
Errors: `400` `quantity must be more than 0`, `403`, `404`, `409` `wastage is more than the stock available`.

---

## 12. Dashboard

### `GET /v1/dashboard?period=today|week|month&location_id=<optional>` — shop

`period` defaults to `today`. Leave out `location_id` for all locations (a farm manager always gets the farm only).

```json
{
  "period": "week",
  "sales": 112600,
  "bills": 298,
  "customers": 184,
  "payments": { "cash": 56300, "upi": 48900, "credit": 7400, "card": 0 },
  "points": { "given": 11260, "redeemed": 3900 },
  "growth_percent": 5,
  "chart": {
    "labels": ["M", "T", "W", "T", "F", "S", "S"],
    "amounts": [13500, 12700, 15000, 12500, 16900, 21800, 20200],
    "best": { "label": "S", "amount": 21800 }
  },
  "rates_updated_today": false,
  "low_stock": [
    { "item_id": "a1745d2e-7514-47f9-a417-3716581fd734", "name": "Broiler", "quantity": 3.2, "unit": "kg", "reorder_level": 5 }
  ]
}
```
- `sales` is the sum of what customers paid (`payable`) for bills in the period. `bills` is the count. `customers` is the number of different customers served (walk-ins are not counted).
- `payments` splits `sales` by payment mode. `points.given` and `points.redeemed` are the points earned and used on those bills.
- **Periods**, in the shop's timezone: `today` is midnight to now, `week` is Monday midnight to now, `month` is the 1st at midnight to now. A bill saved in the last few minutes counts even if the phone's clock runs a little fast.
- `growth_percent` compares with the same span before it: yesterday up to the same time, last week's same elapsed span, last month's same elapsed span. It is `round((current - previous) / previous x 100)`, and `null` when the previous period had no sales.
- **Chart buckets** are always all present, `0` where there were no sales:
  - `today`: 7 two-hour buckets `8a, 10a, 12p, 2p, 4p, 6p, 8p`. Sales before 8am go in the first, 8pm and later in the last.
  - `week`: `M T W T F S S`. Days not reached yet are 0.
  - `month`: `W1` (days 1 to 7), `W2` (8 to 14), `W3` (15 to 21), `W4` (22 to 28) and `W5` (29 to the end, only in months with 29 or more days).
- `best` is the highest bucket (the first on a tie), or `null` when everything is 0.
- `rates_updated_today` is true when any price was saved today.
- `low_stock` has up to 10 items with a reorder level where `quantity <= reorder_level`, lowest ratio first.

Errors: `400` `period must be today, week or month`, `invalid location_id`, `unknown location`. `403` for a farm manager asking for a shop location.

---

## 13. Setup checklist (new-shop dashboard)

### `GET /v1/setup` — shop

```json
{
  "steps": [
    { "id": "shop", "done": true },
    { "id": "prices", "done": false },
    { "id": "stock", "done": false },
    { "id": "bill", "done": false }
  ],
  "done_count": 1, "total": 4, "percent": 25, "minutes_left": 6, "complete": false
}
```
A step is done when the shop's data proves it **or** the owner ticked it. `shop` is always done. `prices` is done after the first saved price, `stock` after the first stock-in, `bill` after the first bill. `minutes_left` is 2 minutes for each step left.

### `PATCH /v1/setup/steps/{id}` — owner

Request `{ "done": true }` or `{ "done": false }`. Response `200` is the same body as `GET /v1/setup`. Un-ticking a step that the data proves leaves it done.
Errors: `400` `unknown step`, `the shop step cannot be changed`, a missing `done`. `403` for non-owners.

---

## 14. Expenses

### `POST /v1/expenses` — owner, cashier, farm manager (farm location only)

```json
{ "location_id": "068dd605-2752-40e6-85f3-34adffe28730", "amount": 1800, "note": "Electricity", "category": "power" }
```
`amount` must be more than 0. `note` (200 characters) and `category` (40) are optional. Response `201`:
```json
{ "id": "8e784675-fbcd-42bd-ae8c-62f42710595f", "location_id": "068dd605-...", "amount": 1800, "note": "Electricity", "category": "power", "created_at": "2026-11-16T19:42:24+05:30" }
```
Errors: `400` `location_id is required`, `amount must be more than 0`, `unknown location`. `403` farm manager on a shop location.

### `GET /v1/expenses?date=YYYY-MM-DD&location_id=` — owner, cashier, farm manager

The day's expenses (default today, shop timezone). Response `200`:
```json
{ "date": "2026-11-16", "total": 720.5, "items": [ { "id": "...", "location_id": "...", "amount": 600, "note": "Electricity", "category": "power", "created_at": "..." } ] }
```
Errors: `400` `date must be YYYY-MM-DD`, `date cannot be in the future`.

---

## 15. Close the day

### `GET /v1/close-day/preview?location_id=<required>&date=YYYY-MM-DD` — owner, cashier

`date` defaults to today.
```json
{
  "date": "2026-11-16", "location_id": "068dd605-...", "bills": 46, "already_closed": false,
  "cash": { "expected": 9200 },
  "other_money": { "upi_received": 8050, "credit_given": 1200, "credit_collected": 800, "expenses": 5000 },
  "stock": [ { "item_id": "f3d10192-...", "name": "Curry cut", "unit": "kg", "expected": 6.2 } ]
}
```
- `cash.expected` = cash bills + cash collected on credit - expenses (no opening float is recorded yet, so it counts as 0).
- `upi_received` is the UPI bills. `credit_given` is the bills paid by credit. `credit_collected` is the credit payments collected at this location. `expenses` is all expenses at this location.
- `stock` lists up to 20 items: those that sold that day first, then those with stock left. `expected` is the current quantity.

### `POST /v1/close-day` — owner, cashier

Supports `Idempotency-Key`. One transaction.
```json
{
  "location_id": "068dd605-...", "date": "2026-11-16", "counted_cash": 9050, "reason": "coins missing",
  "stock": [ { "item_id": "f3d10192-...", "actual": 5.9 } ]
}
```
`counted_cash` is required and must be 0 or more. `date` defaults to today and cannot be in the future. `stock` and `reason` are optional; every `actual` must be 0 or more and each `item_id` may appear once.

What it does:
- `difference = counted_cash - expected` (negative means short).
- For each counted stock item, the stock quantity is set to `actual`. For `kg` items the missing part is recorded as **wastage** (`max(expected - actual, 0)`). A short count of birds, trays or pieces is a stock correction, not wastage.
- It saves the closing, and queues a day summary for the owner's WhatsApp (nothing is sent yet).

Response `201`
```json
{ "id": "0577b7f3-080e-4f9e-8c65-7995279aa5d6", "difference": -150, "status": "short", "wastage": [ { "name": "Curry cut", "quantity": 0.3, "unit": "kg" } ] }
```
`status` is `short`, `extra` or `matched`.
Errors: `400` `counted_cash is required`, `counted_cash must be 0 or more`, `stock 1: actual must be 0 or more`, `unknown stock item for this location`, `date cannot be in the future`. `409` `this day is already closed` (one close per location per date).

---

## 16. Rewards ("Reward your buyers")

### `GET /v1/rewards` — shop

```json
{
  "spend_per_point": 10, "point_value": 0.25, "expiry_months": 12, "max_pay_percent": 50,
  "items": [ { "id": "a754545a-7a93-4107-8c81-e2a3aa35b572", "name": "1 tray brown eggs", "points": 300 } ]
}
```
A customer earns 1 point for every `spend_per_point` rupees paid. Each point is worth `point_value` rupees. Points can pay at most `max_pay_percent` of a bill.

### `PUT /v1/rewards` — owner

Request: all four values are required. Response `200` is the same body as `GET`.
| Field | Allowed |
|---|---|
| `spend_per_point` | whole number, 1 to 1000 |
| `point_value` | 0.05 to 5 |
| `expiry_months` | whole number, 1 to 60 |
| `max_pay_percent` | whole number, 5 to 100 |

The change applies to bills saved after it, never to old ones. Errors: `400` with the rule that failed.

### `POST /v1/rewards/items` — owner

Request `{ "name": "1 tray brown eggs", "points": 300 }` (`points` is a whole number from 1 to 1000000). Response `201` is `{ "id", "name", "points" }`. `409` `reward already exists: <name>` (spelling variants count).

### `DELETE /v1/rewards/items/{id}` — owner

Response `204`. `404` `reward not found`.

---

## 17. Voice entry

### `POST /v1/voice/parse` — shop

Turns what the owner said into rows for the confirm popup. **It saves nothing.** After the user confirms, the app calls the normal endpoint: `POST /v1/stock/in` with `"source": "voice"` for `stock_in`, `POST /v1/bills` for `sale`, `POST /v1/expenses` for `expense`.

Request
```json
{ "transcript": "Aaj 30 kilo rohu aur 10 tray ande aaye, rohu 140 rupaye kilo", "mode": "stock_in", "language": "hi" }
```
| Field | Required | Notes |
|---|---|---|
| `transcript` | yes | up to 1000 characters |
| `mode` | yes | `stock_in`, `sale` or `expense` |
| `language` | no | `en` (default), `hi` or `or` |

Response `200` for `stock_in` and `sale`:
```json
{
  "items": [
    { "name": "Rohu fish", "heard": "rohu", "matched": true, "quantity": 30, "unit": "kg", "rate": 140 },
    { "name": "Eggs", "heard": "ande", "matched": true, "quantity": 10, "unit": "tray", "rate": null }
  ]
}
```
- `name` is the shop's own item name when one matches, otherwise what was heard. `heard` is the spoken words. `matched` is true when `name` is an item the shop already has.
- `unit` is `kg`, `tray`, `piece` or `bird`, or `null` when it could not be told. When the user did not say a unit but the item is known, the item's unit is used.
- `rate` is `null` when no price was said. In `sale` mode a missing rate is filled with the shop's price for a matched item.
- Dozens are converted to pieces (2 dozen becomes `24` `piece`). Grams are converted to kg (500 gram becomes `0.5` kg).
- Words that are not items, numbers or units (aaj, aaye, ka, and so on) are ignored. A number with no item name is dropped.

Response `200` for `expense`:
```json
{ "items": [ { "name": "Bijli bill", "heard": "bijli bill", "amount": 1800 } ] }
```
If nothing could be understood, `items` is `[]`.

It understands English, Hindi and Odia number words (ek, do, teen, dhai, adha, sau, hazaar, ...), digits in Devanagari and Odia, units (kilo, kg, gram, tray, piece, dozen), rupee words (rupaye, rupees, rs, the rupee sign), and Hindi and Odia item words (anda = egg, murga = chicken, machhi = fish, bakra = mutton). The parser is rule based, and a smarter one can replace it later without changing this API.
Errors: `400` `transcript is required`, `mode must be stock_in, sale or expense`, `language must be en, hi or or`.

---

## 18. Profile screen

One call fills the screen (`GET /v1/profile`); every sheet then has its own endpoint. All errors use `{ "error": "message" }`.

### `GET /v1/profile` — shop (any role)

Response `200`
```json
{
  "user":  { "id": "uuid", "name": "Ravi Kumar", "phone": "9000000001", "photo_url": null, "language": "or" },
  "shop":  { "id": "uuid", "name": "Soumya Poultry", "logo_url": null, "language": "or", "role": "owner",
             "categories": ["chicken", "fish", "eggs"],
             "locations": [
               { "id": "uuid", "type": "farm", "name": "Poultry farm", "lat": 20.29, "lng": 85.82, "active": true },
               { "id": "uuid", "type": "shop", "name": "Shop", "lat": null, "lng": null, "active": true }
             ] },
  "prices":  { "count": 12, "updated_today": true },
  "offer":   { "enabled": false, "type": "discount", "discount_amount": 20, "min_bill": 500, "product": null, "multiplier": 2 },
  "rewards": { "spend_per_point": 10, "point_value": 0.25, "expiry_months": 12, "max_pay_percent": 50 },
  "staff":   { "count": 2 },
  "plan":    { "plan": "trial", "trial_days_left": 11, "trial_ends_at": "2026-12-01T17:06:22+05:30",
               "current_period_end": null, "last_backup_at": null },
  "devices": { "whatsapp": false, "printer": false, "scale": false, "all_connected": false }
}
```
- `plan` is `null` unless the caller is the owner. A farm manager sees only the farm in `shop.locations`.
- `staff.count` counts active members and pending invites.
- `trial_days_left` = `max(0, ceil((trial_ends_at - now) / 24h))`, and `0` when the plan is not `trial`. The trial is 14 days from shop creation (`TRIAL_DAYS` on the server).
- `offer` is created with defaults the first time it is read. `rewards` is the same data as `GET /v1/rewards`: the server value wins over any default the app shows.
- The shop `name` is the business name that prints on bills.

### `PATCH /v1/me` — token

Request (all optional, send at least one)
```json
{ "name": "Ravi Kumar", "photo_url": "https://…/avatar.jpg", "language": "hi" }
```
- `name`: trimmed, 1 to 80 characters.
- `photo_url`: the `url` from `POST /v1/uploads/logo` (use the same upload for the avatar), or `null` to remove it.
- `language`: `en`, `hi`, `or` or `hinglish`. This is the user's own app language.
- The phone cannot be changed.

Response `200`: the `user` object above. Errors: `400` empty body, blank name, unknown language, bad `photo_url`.

### `PATCH /v1/shops/current` — owner

Request (all optional)
```json
{ "name": "Soumya Poultry", "logo_url": "https://…/logo.png", "language": "or" }
```
`logo_url` follows the same rule as `photo_url` (`null` removes it). `language` is the shop language used for WhatsApp messages to customers. Response `200`: the `shop` object above.

### `PUT /v1/shops/current/categories` — owner ("What you sell")

Request
```json
{ "categories": ["chicken", "fish", "eggs"] }
```
Allowed: `chicken`, `fish`, `eggs`, `mutton`, `dairy`. Duplicates are removed and an empty list is allowed. Items and history are never deleted when a category is turned off, but the server hides them: `GET /v1/categories`, `GET /v1/prices` and `GET /v1/prices/today` no longer return that category or its items until it is turned on again (also turned on when you add an item to it). Custom categories are always shown. Bills, stock and past data are not affected. Response `200`: `{ "categories": ["chicken", "fish", "eggs"] }`. Errors: `400` `unknown category: pizza`.

### `GET /v1/locations` — shop

Response `200`: `[ { "id", "type", "name", "lat", "lng", "active" } ]`. Inactive locations are included with `active: false`. A farm manager only gets the farm.

### `PUT /v1/locations` — owner ("Shops & farms")

Request
```json
{ "has_farm": true, "has_shop": true,
  "farm_location": { "lat": 20.29, "lng": 85.82 },
  "shop_location": null }
```
- `has_farm` and `has_shop` are both required, and at least one must be true (`400` `pick at least one location`).
- Turning one on creates it ("Poultry farm" or "Shop") or re-activates the old row. Turning one off sets `active: false`. Nothing is deleted, because bills, stock and closings point at it.
- A position needs both `lat` (-90..90) and `lng` (-180..180), and is only allowed when that location is on. Leaving it out or sending `null` keeps the old position.

Response `200`: the same list as `GET /v1/locations`.

### `GET /v1/offer` — shop, and `PUT /v1/offer` — owner (offer of the day)

`PUT` replaces the whole offer.
```json
{ "enabled": true, "type": "discount", "discount_amount": 20, "min_bill": 500, "product": null, "multiplier": 2 }
```
- `type` is `discount` or `points`.
- `discount`: `discount_amount` 5 to 1000 in steps of 5, `min_bill` 50 to 100000 in steps of 50.
- `points`: `product` is required (for example `chicken`, `fish`, `eggs`, `mutton`, `dairy`), `multiplier` is 2, 3 or 5.
- Fields of the other type are stored but not checked.

Response `200`: the offer. Errors: `400` with the field and range.

### `GET /v1/offer/preview?bill_total=620` — shop

Used on New bill. Response `200`
```json
{ "applies": true, "discount": 20, "type": "discount", "multiplier": 2, "product": null }
```
`applies` is true when a discount offer is on and `bill_total >= min_bill`. The bill itself does not apply the offer yet: the app shows `discount` or the points multiplier (see "Not available yet").

### Reward points

Use `GET /v1/rewards` and `PUT /v1/rewards` from section 16. The values are `spend_per_point` 1 to 1000, `point_value` 0.05 to 5, `expiry_months` 1 to 60, `max_pay_percent` 5 to 100. Changes only affect new bills.

### Today's prices sheet

- `GET /v1/prices/today` now also returns `price_yesterday` per item.
- `PATCH /v1/items/{id}` (owner) with `{ "name", "unit", "category", "active" }`, all optional. A duplicate name in the same category returns `409`. It is the same as `PATCH /v1/prices/items/{id}`, but owner only.
- Moving an item to another category: send `category` (`chicken`, `fish`, `eggs`, `mutton`, `dairy`, `other`, or a custom category name) or `category_id` (from `GET /v1/categories`). The item keeps its prices. If the name already exists in that category you get `409`. A name that is not a known category creates a custom category, like on create. Moving an item into a standard category switches that category on for the shop.
- `DELETE /v1/items/{id}` (owner) hides the item (`204`). Old bills and price history keep working. `404` when the item is not in this shop.

### `GET /v1/staff` — owner, cashier

Owners first, then active members, then pending invites.
```json
[
  { "id": "uuid", "user_id": "uuid", "name": "Ravi", "phone": "9000000001", "role": "owner", "status": "active" },
  { "id": "uuid", "user_id": null, "name": "Mgr", "phone": "9000000003", "role": "farm_manager", "status": "invited" }
]
```
`id` is what `PATCH` and `DELETE` take. For an active member it is the `user_id`, for an invite it is the invite id. The app labels: `owner` is "Owner", `cashier` is "Cashier", `farm_manager` is "Manager".

### `POST /v1/staff/invite` — owner

Request `{ "phone": "9000000002", "role": "cashier", "name": "Ravi" }` (`role`: `cashier` or `farm_manager`, `phone`: 10 digits, `name` optional).

Response `201`: a staff row. If that phone already has an account the member is added at once (`status: "active"`). Otherwise `status: "invited"`, and the invite is accepted automatically the first time that phone logs in. Errors: `400` bad phone or role, `400` `an owner cannot be invited`, `409` `already a member`.

### `PATCH /v1/staff/{id}` — owner

Request `{ "role": "cashier" }`. Response `200`: the staff row. Errors: `409` `the last owner cannot be changed`, `404`.

### `DELETE /v1/staff/{id}` — owner

`204`. The person loses access immediately: their next request for this shop returns `403`. A pending invite is simply cancelled. Errors: `409` `the last owner cannot be removed`, `404`.

### `GET /v1/devices` — shop, and `PUT /v1/devices/{device}` — owner, cashier

`device` is `whatsapp`, `printer` or `scale`. Printer and scale are paired on the phone, so the phone is the source of truth and the server only remembers the status for other staff phones.
```json
{ "whatsapp": { "connected": false, "phone": null },
  "printer":  { "connected": true,  "name": "TVS-3230" },
  "scale":    { "connected": false, "name": null } }
```
`PUT` request: `{ "connected": true, "name": "TVS-3230" }` (`connected` required, `name` or `phone` optional). Response `200`: the same object as `GET`. Errors: `400` unknown device or `connected` missing.

### `POST /v1/devices/whatsapp/test` — owner

Queues a `whatsapp_test` message for the owner's phone. Response `200` `{ "queued": true }`. Nothing is sent yet (see "Not available yet").

### `GET /v1/plan` — owner

```json
{ "plan": "trial", "trial_days_left": 11, "trial_ends_at": "2026-12-01T17:06:22+05:30",
  "current_period_end": null, "last_backup_at": null }
```
`plan` is `trial`, `basic` or `pro`. Shops made before plans existed start their 14 day trial on first read.

### `POST /v1/plan/upgrade` — owner

Always `501` `{ "error": "payments not enabled" }` for now.

### `POST /v1/backups` — owner, and `GET /v1/backups/latest` — owner

`POST` returns `202` `{ "id": "uuid", "status": "queued" }`. A background job zips the shop's data (CSV files) into private storage. Only one backup per 5 minutes per shop: otherwise `429` with a `Retry-After: 300` header. `503` when file storage is not configured.

`GET /v1/backups/latest` returns `200` `{ "status": "done", "created_at": "…" }` (`status` can also be `queued` or `failed`), or `404` `no backup yet`.

### `POST /v1/exports` — owner, and `GET /v1/exports/{id}` — owner

`POST` with `{ "format": "csv" }` returns `202` `{ "id": "uuid", "status": "queued" }`. Poll `GET /v1/exports/{id}` until `status` is `ready`:
```json
{ "id": "uuid", "status": "ready", "url": "https://…/object/sign/…?token=…", "expires_in": 900 }
```
`url` is a signed download link for a zip with `bills.csv`, `bill_lines.csv`, `customers.csv`, `stock.csv` and `prices.csv`, valid for 15 minutes. Ask again for a fresh link. Only this shop's data is included. `status` can also be `queued` or `failed`. Errors: `400` format is not `csv`, `404`.

### `POST /auth/logout` — token

Send the same `Authorization: Bearer` token you want to end. Response `204`. The server stores a hash of that token until its normal expiry, and from then on every request with it returns `401` `{ "error": "token was logged out" }`. Other tokens of the same user (other phones) keep working. After logout the app deletes the token and shows the login screen. Logging in again gives a new token. Calling logout again with the dead token returns `401`.

---

## Typical app flow

1. `POST /auth/otp/send`, then `POST /auth/otp/verify` and keep `access_token`.
2. `GET /v1/me`. If `shops` is empty, show onboarding. When the user picks a logo, call `POST /v1/uploads/logo` and keep the `url`. On Submit, call `POST /v1/shops` with that `logo_url` and keep the returned `locations`. Otherwise pick a shop.
3. Send `Authorization` and `X-Shop-ID` on everything below.
4. New shop: show `GET /v1/setup` as the checklist. Prices: `POST /v1/prices/items/bulk` (ready list) or `POST /v1/prices/items`, and `PUT /v1/prices` each morning. Stock: `POST /v1/stock/in`.
5. Home screen: `GET /v1/dashboard?period=today`.
6. Per customer: `GET /v1/customers?q=`. To redeem points, call `/v1/points/otp/send` then `/v1/points/otp/verify`.
7. `POST /v1/bills` with an `Idempotency-Key`. Retry with the same key if the network drops.
8. Voice: `POST /v1/voice/parse`, show the rows, and on confirm call the save endpoint for that mode.
9. End of day: `GET /v1/close-day/preview`, then `POST /v1/close-day` with the counted cash and stock.

## Not available yet

- The offer of the day is stored and previewed, but `POST /v1/bills` does not apply it yet (no automatic discount or points multiplier).
- Logging out every device at once.
- Sending WhatsApp messages. The rate broadcast and the owner's day summary are queued only.
- Recording credit payments from customers (the close-day screen already counts them once they exist).
- An opening cash float for close-day.
- Editing the shop timezone, bill history, and sync.
