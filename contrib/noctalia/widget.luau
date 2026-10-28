--!nonstrict
-- Earshot bar toggle. Left click starts/stops recording, right click opens the web UI.

local BASE = "http://127.0.0.1:8787"

local function fmtElapsed(secs)
  local hrs = secs // 3600
  if hrs > 0 then
    return string.format("%d:%02d:%02d", hrs, (secs % 3600) // 60, secs % 60)
  end
  return string.format("%d:%02d", secs // 60, secs % 60)
end

local function render(status)
  if not status then
    barWidget.setGlyph("microphone-off")
    barWidget.setGlyphColor("on_surface_variant")
    barWidget.setText("")
    barWidget.setTooltip("Earshot daemon not running")
  elseif status.recording then
    barWidget.setGlyph("microphone")
    barWidget.setGlyphColor("error")
    barWidget.setText(fmtElapsed(status.elapsed or 0))
    barWidget.setColor("error")
    barWidget.setTooltip({
      { "Earshot", "recording" },
      { "Listeners", tostring(status.listeners) },
      { "Click", "stop · right click: open" },
    })
  else
    barWidget.setGlyph("microphone-off")
    barWidget.setGlyphColor("on_surface")
    barWidget.setText("")
    barWidget.setTooltip({
      { "Earshot", "idle" },
      { "Click", "start · right click: open" },
    })
  end
end

local function handle(res)
  if res and res.ok then
    render(noctalia.json.decode(res.body))
  else
    render(nil)
  end
end

function update()
  noctalia.setUpdateInterval(1000)
  noctalia.http({ url = BASE .. "/api/status" }, handle)
end

function onClick()
  noctalia.http({ url = BASE .. "/api/toggle", method = "POST" }, handle)
end

function onRightClick()
  noctalia.runAsync({ "xdg-open", BASE })
end
