import express from 'express'

const app = express()
const version = 'standalone-node-v1'

app.get('/health', (_request, response) => response.json({ status: 'ok', version }))
app.get('/version', (_request, response) => response.json({
  version,
  runtimeMarker: process.env.RUNTIME_MARKER || 'unset',
  secretPresent: Boolean(process.env.TEST_SENTINEL_SECRET),
}))

const port = Number(process.env.PORT || '3000')
app.listen(port, '0.0.0.0')
