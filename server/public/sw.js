// يخلي البرنامج يتثبت كتطبيق، ويعرض رسالة واضحة إذا البرنامج مو شغال
const OFFLINE = `<!DOCTYPE html><html lang="ar" dir="rtl"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>تقارير الحضور</title></head>
<body style="font-family:Tahoma,sans-serif;background:#f4f6f8;color:#1d2733;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0;padding:20px;box-sizing:border-box">
<div style="background:#fff;border:1px solid #e3e7eb;border-radius:12px;padding:22px;max-width:420px;line-height:1.9">
<h2 style="margin-top:0">البرنامج مو شغال</h2>
<p>شغّل برنامج zk-attendance أولاً. بالموبايل: افتح تطبيق Termux واكتب <b dir="ltr">./zk-attendance</b></p>
<button onclick="location.reload()" style="padding:10px 18px;border:0;border-radius:8px;background:#1f6feb;color:#fff;font-size:16px">إعادة المحاولة</button>
</div></body></html>`;
self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));
self.addEventListener('fetch', (e) => {
  if (e.request.mode !== 'navigate') return;
  e.respondWith(fetch(e.request).catch(() => new Response(OFFLINE, { headers: { 'Content-Type': 'text/html; charset=utf-8' } })));
});
