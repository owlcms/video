# Replays for broadcasters

The venue provides an HTTPS address and an access token for each platform you
cover. Treat each token and any URL containing it as a password. Tokens expire
after 21 days or when the venue revokes or regenerates them.

## Browse replays

Open the platform's address, enter the token in **Access token** and select
**Sign in**. Alternatively, open the sign-in link supplied by the venue. After
sign-in, the browser keeps a session until the token expires or is revoked.
**Sign out**, at the top right of the replays page, removes the session from
this browser only. Hover over it to see which token you signed in with and
when it expires.

## Players and scripts

Include `?token=<token>` in every replay or video URL, or send an
`Authorization: Bearer <token>` header. WebSocket clients can use
`wss://<platform-host>/ws?token=<token>`. URLs returned by the API do not
include your token: add it to each subsequent request.

Ask the venue for a replacement if the token expires or is rejected. Other
broadcasters' tokens are managed independently.
