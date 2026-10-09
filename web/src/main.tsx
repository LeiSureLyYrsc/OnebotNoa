import { render } from "preact";
import "xp.css/dist/XP.css";
import "./styles/theme.css";
import { App } from "./app";

render(<App />, document.getElementById("app")!);
