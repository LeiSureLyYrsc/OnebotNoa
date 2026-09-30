import { render } from "preact";
// xp.css 0.2.6 ships dist/XP.css (there is no pre-minified file in the package).
import "xp.css/dist/XP.css";
import "./styles/theme.css";
import { App } from "./app";

const root = document.getElementById("app");
if (!root) {
  throw new Error("#app container is missing from index.html");
}
render(<App />, root);
