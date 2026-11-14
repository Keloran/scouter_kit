package scouterkit.chewedfeed

import android.app.Activity
import android.content.res.ColorStateList
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.graphics.drawable.RippleDrawable
import android.view.View
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.Switch
import android.widget.TextView

/** Shared native controls, spacing and surfaces; colours follow the system's light/dark setting. */
internal class AppUi(val activity: Activity) {
    fun dp(value: Int) = (value * activity.resources.displayMetrics.density).toInt()
    fun color(id: Int) = activity.getColor(id)
    fun column() = LinearLayout(activity).apply { orientation = LinearLayout.VERTICAL }
    private fun surface(fill: Int, radius: Int = 24) = GradientDrawable().apply {
        setColor(color(fill))
        cornerRadius = dp(radius).toFloat()
    }

    fun text(label: Int, size: Float = 14f, muted: Boolean = true) = TextView(activity).apply {
        setText(label)
        textSize = size
        setTextColor(color(if (muted) R.color.ink_secondary else R.color.ink))
        setLineSpacing(dp(3).toFloat(), 1f)
        setPadding(0, dp(4), 0, dp(8))
    }

    fun heading(label: Int) = text(label, 22f, false).apply {
        typeface = Typeface.create("sans-serif-medium", Typeface.NORMAL)
        isAccessibilityHeading = true
    }

    fun screen(title: Int, subtitle: Int): LinearLayout = column().apply {
        setPadding(dp(20), dp(20), dp(20), dp(28))
        addView(text(R.string.brand_eyebrow, 12f).apply { letterSpacing = .16f })
        addView(heading(title).apply { textSize = 32f })
        addView(text(subtitle))
    }

    fun scroll(content: LinearLayout) = ScrollView(activity).apply {
        isFillViewport = true
        clipToPadding = false
        setBackgroundColor(color(R.color.canvas))
        addView(content)
    }

    fun card(parent: LinearLayout, title: Int, subtitle: Int? = null): LinearLayout {
        val box = column().apply {
            background = surface(R.color.surface)
            setPadding(dp(20), dp(18), dp(20), dp(18))
        }
        parent.addView(box, LinearLayout.LayoutParams(-1, -2).apply { topMargin = dp(16) })
        box.addView(heading(title))
        subtitle?.let { box.addView(text(it)) }
        return box
    }

    fun button(label: Int, primary: Boolean = false, action: () -> Unit) = Button(activity).apply {
        setText(label)
        isAllCaps = false
        textSize = 15f
        typeface = Typeface.create("sans-serif-medium", Typeface.NORMAL)
        minHeight = dp(52)
        setPadding(dp(16), dp(10), dp(16), dp(10))
        setTextColor(color(if (primary) R.color.on_accent else R.color.accent))
        background = RippleDrawable(ColorStateList.valueOf(color(R.color.ripple)),
            surface(if (primary) R.color.accent else R.color.accent_soft, 16), null)
        layoutParams = LinearLayout.LayoutParams(-1, -2).apply { topMargin = dp(10) }
        setOnClickListener { action() }
    }

    @Suppress("UseSwitchCompatOrMaterialCode") // Uses platform widgets throughout; no AndroidX dependency.
    fun toggle(label: Int, checked: Boolean) = Switch(activity).apply {
        id = label
        setText(label)
        isChecked = checked
        textSize = 15f
        setTextColor(color(R.color.ink))
        minHeight = dp(56)
        switchPadding = dp(16)
        setPadding(0, dp(8), 0, dp(8))
    }

    fun field(parent: LinearLayout, label: Int, value: String, type: Int): EditText {
        parent.addView(text(label, 13f).apply { setPadding(0, dp(14), 0, dp(6)) })
        return EditText(activity).apply {
            id = label
            inputType = type
            setText(value)
            textSize = 16f
            setTextColor(color(R.color.ink))
            background = surface(R.color.field, 12)
            minHeight = dp(52)
            setPadding(dp(14), dp(12), dp(14), dp(12))
            layoutParams = LinearLayout.LayoutParams(-1, -2)
            parent.addView(this)
            (parent.getChildAt(parent.childCount - 2) as TextView).labelFor = id
        }
    }

    fun disclosure(parent: LinearLayout, title: Int, subtitle: Int): LinearLayout {
        val box = card(parent, title)
        box.removeViewAt(0)
        val body = column().apply { visibility = View.GONE }
        val control = button(R.string.expand_section) {
            val opening = body.visibility != View.VISIBLE
            body.visibility = if (opening) View.VISIBLE else View.GONE
            (box.getChildAt(1) as Button).apply {
                text = activity.getString(if (opening) R.string.section_open else R.string.section_closed,
                    activity.getString(title))
                contentDescription = activity.getString(if (opening) R.string.collapse_named else R.string.expand_named,
                    activity.getString(title))
            }
        }
        control.text = activity.getString(R.string.section_closed, activity.getString(title))
        control.contentDescription = activity.getString(R.string.expand_named, activity.getString(title))
        box.addView(text(subtitle))
        box.addView(control)
        box.addView(body)
        return body
    }

    fun styleStatus(view: TextView, prominent: Boolean = false) {
        view.textSize = if (prominent) 24f else 14f
        view.setTextColor(color(if (prominent) R.color.ink else R.color.ink_secondary))
        view.setLineSpacing(dp(3).toFloat(), 1f)
        view.setPadding(0, dp(6), 0, dp(8))
        if (prominent) view.typeface = Typeface.create("sans-serif-medium", Typeface.NORMAL)
    }
}
