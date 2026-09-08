(() => {
  const countdown = document.getElementById("countdown");
  const message = document.getElementById("close-message");
  const fallback = document.getElementById("fallback");
  const closingCopy = document.getElementById("closing-copy");
  const seconds = document.getElementById("seconds");

  // Browsers only allow scripts to close windows that were opened by script.
  // The normal CLI launch uses the OS browser command and has no opener.
  if (window.opener === null) {
    message.hidden = true;
    fallback.hidden = false;
    return;
  }

  closingCopy.textContent = "This tab will close in";
  countdown.hidden = false;
  seconds.hidden = false;
  let remaining = 3;
  const timer = window.setInterval(() => {
    remaining -= 1;
    countdown.textContent = String(remaining);
    if (remaining === 0) {
      window.clearInterval(timer);
      message.hidden = true;
      window.close();
      window.setTimeout(() => { fallback.hidden = false; }, 250);
    }
  }, 1000);
})();
