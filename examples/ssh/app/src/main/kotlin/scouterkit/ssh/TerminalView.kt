package scouterkit.ssh

import android.content.Context
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Paint
import android.graphics.Typeface
import android.view.GestureDetector
import android.view.MotionEvent
import android.view.ScaleGestureDetector
import android.view.View
import java.nio.ByteBuffer
import java.nio.ByteOrder

// The phone's mirror of the terminal (R16): the same grid as the glasses, Departure Mono Nerd, pinch to zoom,
// drag to pan. No keyboard: keys come from keyer only.
class TerminalView(context: Context) : View(context) {
    class Cell(val text: String, val fg: Int, val bg: Int, val attrs: Int)
    class Screen(val cols: Int, val rows: Int, val cursorRow: Int, val cursorCol: Int, val cursorVisible: Boolean,
                 val title: String, val cells: Array<Cell>)

    var screen: Screen? = null
        set(value) {
            field = value
            invalidate()
        }

    private val paint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        typeface = Typeface.createFromAsset(context.assets, "departure_mono_nerd.otf")
    }
    private val advance: Float // per unit of text size
    private var zoom = 1f
    private var panX = 0f
    private var panY = 0f
    private val scaler = ScaleGestureDetector(context, object : ScaleGestureDetector.SimpleOnScaleGestureListener() {
        override fun onScale(d: ScaleGestureDetector): Boolean {
            zoom = (zoom * d.scaleFactor).coerceIn(1f, 6f)
            invalidate()
            return true
        }
    })
    private val panner = GestureDetector(context, object : GestureDetector.SimpleOnGestureListener() {
        override fun onScroll(e1: MotionEvent?, e2: MotionEvent, dx: Float, dy: Float): Boolean {
            panX += dx
            panY += dy
            invalidate()
            return true
        }

        override fun onDoubleTap(e: MotionEvent): Boolean {
            zoom = 1f
            panX = 0f
            panY = 0f
            invalidate()
            return true
        }
    })

    init {
        paint.textSize = 100f
        advance = paint.measureText("M") / 100f
        isFocusable = false
    }

    override fun onTouchEvent(e: MotionEvent): Boolean {
        scaler.onTouchEvent(e)
        panner.onTouchEvent(e)
        return true
    }

    override fun onDraw(canvas: Canvas) {
        canvas.drawColor(Color.BLACK)
        val s = screen ?: return
        val cellW = width * zoom / s.cols
        val cellH = cellW * 2
        paint.textSize = cellW / advance
        val maxX = (s.cols * cellW - width).coerceAtLeast(0f)
        val maxY = (s.rows * cellH - height).coerceAtLeast(0f)
        panX = panX.coerceIn(0f, maxX)
        panY = panY.coerceIn(0f, maxY)
        canvas.save()
        canvas.translate(-panX, -panY)
        val baseline = cellH * 0.78f
        for (r in 0 until s.rows) {
            for (c in 0 until s.cols) {
                val cell = s.cells[r * s.cols + c]
                val cursor = s.cursorVisible && r == s.cursorRow && c == s.cursorCol
                var fg = colour(cell.fg, true, cell.attrs and BOLD != 0)
                var bg = colour(cell.bg, false, false)
                if ((cell.attrs and INVERSE != 0) != cursor) fg = bg.also { bg = fg }
                val x = c * cellW
                val y = r * cellH
                if (bg != Color.BLACK) {
                    paint.color = bg
                    canvas.drawRect(x, y, x + cellW * (if (cell.attrs and WIDE != 0) 2 else 1), y + cellH, paint)
                }
                if (cell.text.isEmpty() || cell.attrs and INVISIBLE != 0) continue
                paint.color = if (cell.attrs and FAINT != 0) (fg and 0xffffff) or (0xa0 shl 24) else fg
                paint.isFakeBoldText = cell.attrs and BOLD != 0
                paint.isUnderlineText = cell.attrs and UNDERLINE != 0
                paint.textSkewX = if (cell.attrs and ITALIC != 0) -0.2f else 0f
                canvas.drawText(cell.text, x, y + baseline, paint)
            }
        }
        canvas.restore()
    }

    companion object {
        const val BOLD = 1
        const val FAINT = 2
        const val ITALIC = 4
        const val UNDERLINE = 8
        const val INVERSE = 16
        const val INVISIBLE = 32
        const val WIDE = 64
        private const val DEFAULT_FG = 0xffd0d0d0.toInt()
        private val ANSI = intArrayOf(
            0x000000, 0xcd0000, 0x00cd00, 0xcdcd00, 0x0000ee, 0xcd00cd, 0x00cdcd, 0xe5e5e5,
            0x7f7f7f, 0xff0000, 0x00ff00, 0xffff00, 0x5c5cff, 0xff00ff, 0x00ffff, 0xffffff,
        )

        // Snapshot colours: 0 default, 1..256 palette index + 1, 0x1000000 | RGB.
        fun colour(v: Int, foreground: Boolean, bold: Boolean): Int {
            if (v == 0) return if (foreground) DEFAULT_FG else Color.BLACK
            if (v and 0x1000000 != 0) return v or (0xff shl 24)
            var i = v - 1
            if (bold && i < 8) i += 8
            val rgb = when {
                i < 16 -> ANSI[i]
                i < 232 -> {
                    val n = i - 16
                    fun level(x: Int) = if (x == 0) 0 else 55 + 40 * x
                    (level(n / 36) shl 16) or (level(n / 6 % 6) shl 8) or level(n % 6)
                }
                else -> (8 + 10 * (i - 232)).let { (it shl 16) or (it shl 8) or it }
            }
            return rgb or (0xff shl 24)
        }

        // Native.snapshot's layout (native/jni.cpp).
        fun parse(bytes: ByteArray): Screen {
            val b = ByteBuffer.wrap(bytes).order(ByteOrder.LITTLE_ENDIAN)
            val cols = b.short.toInt() and 0xffff
            val rows = b.short.toInt() and 0xffff
            val cursorRow = b.short.toInt() and 0xffff
            val cursorCol = b.short.toInt() and 0xffff
            val flags = b.get().toInt()
            val title = ByteArray(b.short.toInt() and 0xffff).also { b.get(it) }.toString(Charsets.UTF_8)
            val cells = Array(cols * rows) {
                val fg = b.int
                val bg = b.int
                val attrs = b.get().toInt() and 0xff
                val text = ByteArray(b.get().toInt() and 0xff).also { b.get(it) }.toString(Charsets.UTF_8)
                Cell(text, fg, bg, attrs)
            }
            return Screen(cols, rows, cursorRow, cursorCol, flags and 1 != 0, title, cells)
        }
    }
}
