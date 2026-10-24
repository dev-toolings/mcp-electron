// An app started like any other: no --remote-debugging-port, no --inspect. Its window stays hidden so the test
// adds nothing on screen; attach by pid must still read and drive it.
const { app, BrowserWindow } = require('electron');
app.whenReady().then(() => {
  const win = new BrowserWindow({ show: false, width: 640, height: 400, webPreferences: { backgroundThrottling: false } });
  win.loadURL('data:text/html,' + encodeURIComponent('<title>pid fixture</title><button id="go" onclick="this.textContent=\'clicked\'">press</button>'));
});
