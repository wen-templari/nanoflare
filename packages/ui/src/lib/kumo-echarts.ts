import * as echarts from "echarts/core";
import { LineChart } from "echarts/charts";
import { AriaComponent, GridComponent, TooltipComponent } from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";

echarts.use([AriaComponent, CanvasRenderer, GridComponent, LineChart, TooltipComponent]);

export { echarts };
